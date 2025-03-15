package persist

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/goccy/go-json"
	"github.com/puzpuzpuz/xsync/v3"
)

// Identification string written at the beginning of each WAL file
// to validate file format and version compatibility
const WalHeader = "go-persist 1"

// Default value for store.syncInterval
const DefaultSyncInterval = time.Second

var (
	// ErrKeyNotFound is returned when the key is not found in the storage
	ErrKeyNotFound = errors.New("key not found")
	ErrNotLoaded   = errors.New("store is not loaded")
)

// Store represents the WAL(write-ahead log) storage
type Store struct {
	mu             sync.Mutex     // protects concurrent access to the file
	f              *os.File       // file descriptor for append operations
	path           string         // file path used for reopening during reads
	stopSync       chan struct{}  // channel to signal background sync to stop
	wg             sync.WaitGroup // waitgroup for background sync goroutine and shrink
	persistMaps    *xsync.Map     // registry of PersistMap instances
	closedMaps     *xsync.Map     // list of map with was Close()
	orphanRecords  *xsync.Map     // stores records that do not belong to any registered map
	syncInterval   atomic.Int64   // sync and flush interval background f.Sync() (representing a time.Duration)
	shrinking      bool           // flag to indicate that a shrink operation is in progress
	pendingRecords []string       // buffer for pending WAL records during shrink (each record already contains header+value+'\n')
	loaded         bool
	ErrorHandler   func(err error)
}

// New creates and initializes a new Store instance.
//
// The returned Store is not yet connected to any file - you must call Open()
// with a file path to load existing data or create a new persistence file.
//
// By default, the Store is configured with:
//
// - DefaultSyncInterval (1 second) for background synchronization
//
// - A default error handler that calls log.Fatal
//
// - Empty maps for tracking PersistMap instances and orphaned records
//
// Example usage:
//
//	store := persist.New()
//	err := store.Open("mydata.db")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer store.Close()
func New() *Store {
	s := &Store{
		persistMaps:   xsync.NewMap(),
		closedMaps:    xsync.NewMap(),
		orphanRecords: xsync.NewMap(),
		stopSync:      make(chan struct{}),
	}
	s.SetSyncInterval(DefaultSyncInterval)

	s.ErrorHandler = func(err error) {
		log.Fatal("go-persist: ", err)
	}

	return s
}

// Open opens the persistent storage file, validates/writes the WAL header,
// starts the background sync goroutine and immediately loads all WAL records
// into the registered maps.
func (s *Store) Open(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return errors.New("store is already loaded")
	}

	var err error
	s.path = path
	// Open file in read/write append mode (create if not exists)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	// Validate or write WAL header
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}

	if stat.Size() == 0 {
		// File is new, write header
		if _, err := f.Write([]byte(WalHeader + "\n")); err != nil {
			f.Close()
			return err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return err
		}
	} else {
		// Validate existing header
		if _, err := f.Seek(0, 0); err != nil {
			f.Close()
			return err
		}
		reader := bufio.NewReader(f)
		headerLine, err := reader.ReadString('\n')
		if err != nil {
			f.Close()
			return err
		}
		if strings.TrimSpace(headerLine) != WalHeader {
			f.Close()
			return errors.New("invalid WAL header, unsupported WAL file")
		}
		// Seek back to the end for appending writes
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			f.Close()
			return err
		}
	}
	s.f = f

	if err := s.processRecords(); err != nil {
		f.Close()
		return err
	}

	// Start background FSyncAll goroutine
	s.wg.Add(1)
	go func() {
		timer := time.NewTimer(s.GetSyncInterval())
		defer timer.Stop()
		defer s.wg.Done()
		for {
			select {
			case <-timer.C:
				// Attempt fsync all maps and file
				if err := s.FSyncAll(); err != nil {
					s.ErrorHandler(fmt.Errorf("background sync failed: %s", err))
				}
				timer.Reset(s.GetSyncInterval())
			case <-s.stopSync:
				return
			}
		}
	}()

	s.loaded = true
	return nil
}

// processRecords reads the WAL file once and dispatches records to all registered PersistMap instances.
// If a record's key does not match any map (determined by the part before the colon), it is stored in orphanRecords.
func (s *Store) processRecords() error {
	f, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer f.Close()

	reader := bufio.NewReader(f)

	// Skip header
	_, _ = reader.ReadString('\n')

	// recordData holds the parsed data for each record
	type recordData struct {
		op, fullKey, valueStr string
	}

	// Create a buffered channel to decouple reading from processing
	recordsChan := make(chan recordData, 100)

	// Start a goroutine for reading the records concurrently
	go func() {
		defer close(recordsChan)
		for {
			op, fullKey, valueStr, err := readRecord(reader)
			if err != nil {
				if err == io.EOF {
					break
				}
				log.Println("go-persist: error reading record:", err)
				break
			}
			recordsChan <- recordData{op: op, fullKey: fullKey, valueStr: valueStr}
		}
	}()

	// Process the records in the same order as they were read
	for rec := range recordsChan {
		idx := strings.Index(rec.fullKey, ":")
		candidate := ""
		if idx >= 0 {
			candidate = rec.fullKey[:idx]
		}

		if mapVal, ok := s.persistMaps.Load(candidate); ok {
			// Registered map found - process the record via its interface
			pm, _ := mapVal.(persistMapI)
			if err := pm.processRecord(rec.op, rec.fullKey[idx+1:], rec.valueStr); err != nil {
				log.Println("go-persist: failed processing record for key", rec.fullKey, "error:", err)
			}
		} else {
			// No matching map – save the raw record as a string in orphanRecords
			switch rec.op {
			case "S":
				s.orphanRecords.Store(rec.fullKey, rec.valueStr)
			case "D":
				s.orphanRecords.Delete(rec.fullKey)
			}
		}
	}

	return nil
}

// Saves all pending changes and stops the background sync goroutine
// Then closes the underlying file.
//
// The Store should not be used after calling Close.
func (s *Store) Close() error {
	if !s.loaded {
		return ErrNotLoaded
	}

	// Signal background FSyncAll to stop and wait for it to finish
	close(s.stopSync)
	s.wg.Wait()

	err := s.FSyncAll()
	if err != nil {
		return err
	}
	s.persistMaps = nil
	s.orphanRecords = nil
	return s.f.Close()
}

// FSyncAll ensures complete data durability by:
//
//  1. Synchronizing all dirty map entries to the WAL file
//  2. Performing an fsync operation to guarantee data is physically written to disk
//
// This operation provides the strongest durability guarantee, protecting against
// both application crashes and system failures. It's automatically called
// periodically based on the configured syncInterval, but can also be called
// manually when immediate durability is required.
func (s *Store) FSyncAll() error {
	if !s.loaded {
		return ErrNotLoaded
	}
	// Sync Maps
	s.persistMaps.Range(func(key string, val interface{}) bool {
		pm, _ := val.(interface{ Sync() })
		pm.Sync()
		return true
	})
	// Flush file
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Sync()
}

// write persists a key-value pair by writing a "set" record to the log.
// The record format consists of two lines:
// 1. S <key>
// 2. <json-serialized-value>
// The newline after the value serves as a marker that the record was
// successfully written and can be safely processed during recovery.
func (s *Store) write(key string, value interface{}) error {
	if !s.loaded {
		return ErrNotLoaded
	}
	if err := ValidateKey(key); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}

	header := "S " + key + "\n"
	line := string(data) + "\n"

	// TODO m.b. RLock? Write syscall for O_APPEND must be threadsafe
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err = s.f.Write([]byte(header + line)); err != nil {
		return err
	}

	// If shrinking is in progress, also append the record into pendingRecords
	if s.shrinking {
		s.pendingRecords = append(s.pendingRecords, header+line)
	}
	return nil
}

// Delete marks a key as deleted by writing a "delete" record to the log.
// The record format consists of two lines:
//  1. D <key>
//  2. <Empty value line>
//
// The newline after the empty value line serves as a marker that the delete
// record was successfully written and can be safely processed during recovery.
func (s *Store) Delete(key string) error {
	if !s.loaded {
		return ErrNotLoaded
	}

	header := "D " + key + "\n"
	line := "\n"

	s.mu.Lock()
	defer s.mu.Unlock()

	defer s.orphanRecords.Delete(key)
	if _, err := s.f.Write([]byte(header + line)); err != nil {
		return err
	}

	// If a shrink is in progress, also record the delete operation in the pending buffer
	if s.shrinking {
		s.pendingRecords = append(s.pendingRecords, header+line)
	}
	return nil
}

// readRecord reads a single WAL record from the provided reader.
// It returns the operation (op), key, value and an error if any.
func readRecord(reader *bufio.Reader) (op string, key string, value string, err error) {
	// Read header line using ReadSlice to avoid extra allocations
	headerLine, err := reader.ReadSlice('\n')
	if err != nil {
		return "", "", "", err
	}

	// Remove trailing
	headerLine = headerLine[:len(headerLine)-1]

	// Expect at least 3 bytes: 1 byte for op, 1 for space and at least 1 for key
	if len(headerLine) < 3 {
		return "", "", "", errors.New("invalid record header: too short")
	}

	// Operation is always the first character
	op = string(headerLine[0])

	// Check that the second character is a space
	if headerLine[1] != ' ' {
		return "", "", "", errors.New("invalid record header format: missing space after operation")
	}

	// Key is the rest of the header
	key = string(headerLine[2:])

	// Read value line (ensure it ends with a newline)
	valueLine, err := reader.ReadSlice('\n')
	if err != nil {
		if err == io.EOF {
			log.Printf("go-persist: incomplete record detected, reached EOF after header: %q, partial value: %q", headerLine, valueLine)
		}
		return "", "", "", err
	}
	valueLine = valueLine[:len(valueLine)-1]
	value = string(valueLine)

	// Log unknown operations if necessary
	if op != "S" && op != "D" {
		log.Println("go-persist: unknown operation encountered:", op)
	}
	return op, key, value, nil
}

// Get retrieves a typed value from orphaned records.
// Returns ErrKeyNotFound if the key doesn't exist or was deleted in the most recent operation.
func Get[T any](s *Store, key string) (T, error) {
	var result T
	if !s.loaded {
		return result, ErrNotLoaded
	}

	data, exists := s.orphanRecords.Load(key)
	if !exists {
		return result, ErrKeyNotFound
	}

	// If the stored value is already of type T, return it directly.
	if typed, ok := data.(T); ok {
		return typed, nil
	}

	// If the stored value is a string, perform lazy JSON unmarshaling.
	dataStr, ok := data.(string)
	if !ok {
		return result, errors.New("stored orphan record is not convertible to expected type")
	}
	err := json.Unmarshal([]byte(dataStr), &result)
	if err != nil {
		return result, fmt.Errorf("failed to unmarshal orphan record: %w", err)
	}

	// Cache the converted result for future calls.
	s.orphanRecords.Store(key, result)
	return result, nil
}

// Set persists a key-value pair by writing a "set" record to the WAL log
// and updates the corresponding entry in orphanRecords.
//
// This is a synchronous operation that writes to the WAL file immediately, but without fsync.
func (s *Store) Set(key string, value interface{}) error {
	err := s.write(key, value)
	if err != nil {
		return err
	}
	s.orphanRecords.Store(key, value)
	return nil
}

// Shrink compacts the WAL file by discarding deleted records and redundant updates,
// retaining only the latest state for each key. The operation is designed to be
// minimally blocking:
//
//  1. Most compaction happens without locks, allowing concurrent operations
//  2. Operations performed during shrinking are captured and preserved
//  3. Only brief locks are used to swap files and finalize pending operations
//
// The function creates a temporary file with current state only, then atomically
// replaces the original WAL file.
func (s *Store) Shrink() error {
	if !s.loaded {
		return ErrNotLoaded
	}
	// Prevent concurrent shrink operations
	s.mu.Lock()
	if s.shrinking {
		s.mu.Unlock()
		return errors.New("shrink operation is already in progress")
	}
	defer func() {
		s.shrinking = false
	}()
	s.shrinking = true
	s.pendingRecords = nil
	s.wg.Add(1)
	defer s.wg.Done()
	s.mu.Unlock()

	// Create temporary file for the compacted WAL
	tmpPath := s.path + ".tmp"
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	// Write the WAL header
	if _, err := tmpFile.WriteString(WalHeader + "\n"); err != nil {
		return err
	}

	// Iterate over orphanRecords and write each record to the temporary file
	var outErr error
	s.orphanRecords.Range(func(key string, value interface{}) bool {
		var valueStr string
		// Determine if the stored orphan record is already a JSON string or needs marshaling
		switch v := value.(type) {
		case string:
			valueStr = v
		default:
			// Marshal value to JSON representation
			marshalled, err := json.Marshal(v)
			if err != nil {
				outErr = fmt.Errorf("failed to marshal orphan record for key %s: %w", key, err)
				return false
			}
			valueStr = string(marshalled)
		}
		// Write set record for key
		if _, err := tmpFile.WriteString("S " + key + "\n"); err != nil {
			outErr = err
			return false
		}
		if _, err := tmpFile.WriteString(valueStr + "\n"); err != nil {
			outErr = err
			return false
		}
		return true
	})
	if outErr != nil {
		return outErr
	}

	// Write persistMap states
	s.persistMaps.Range(func(mapName string, pmInterface interface{}) bool {
		if pm, ok := pmInterface.(persistMapI); ok {
			if err := pm.writeRecords(tmpFile); err != nil {
				outErr = err
				return false
			}
		}
		return true
	})
	if outErr != nil {
		return outErr
	}

	// Sync file to disk before obtaining lock to minimize lock duration
	if err := tmpFile.Sync(); err != nil {
		return err
	}

	// Drain pendingRecords (operations performed during shrink) and write them.
	// Use a loop to quickly swap out pendingRecords up to 3 times to minimize
	// lock contention while still capturing most operations
	for i := 0; i < 3; i++ {
		s.mu.Lock()
		if len(s.pendingRecords) == 0 {
			s.mu.Unlock()
			break
		}
		// Copy pendingRecords to a local variable
		localPending := s.pendingRecords
		s.pendingRecords = nil // clear pending records quickly
		s.mu.Unlock()

		// Write the locally copied pending records outside the lock
		for _, rec := range localPending {
			if _, err := tmpFile.WriteString(rec); err != nil {
				return err
			}
		}
		if err := tmpFile.Sync(); err != nil {
			return err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Process any remaining pendingRecords under final lock to ensure all operations are captured before file swap
	for _, rec := range s.pendingRecords {
		if _, err := tmpFile.WriteString(rec); err != nil {
			return err
		}
	}
	s.pendingRecords = nil

	// Flush all writes to disk
	if err := tmpFile.Sync(); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	// Replace the old WAL: close current file, atomically rename the temporary file, and reopen the WAL
	if err := s.f.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, s.path); err != nil {
		return err
	}

	newFile, err := os.OpenFile(s.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	s.f = newFile

	return nil
}

// GetSyncInterval returns the current sync interval
func (s *Store) GetSyncInterval() time.Duration {
	return time.Duration(s.syncInterval.Load())
}

// SetSyncInterval sets a new sync interval
func (s *Store) SetSyncInterval(interval time.Duration) {
	s.syncInterval.Store(int64(interval))
}

// ValidateKey validates the provided key ensuring it is not empty and that it does not include forbidden characters:
// ASCII (0x00–0x1F, 0x7F) and additional ones in the extended control range (0x80–0x9F).
func ValidateKey(key string) error {
	// Iterate over the string using indexing to avoid extra allocations for pure ASCII strings
	for i := 0; i < len(key); i++ {
		b := key[i]
		// Fast path for ASCII characters
		if b < 0x80 {
			// b is an ASCII character
			// Check for control characters (0x00-0x1F and 0x7F)
			if b < 0x20 || b == 0x7F {
				return fmt.Errorf("key contains forbidden control character (byte 0x%x) at position %d", b, i)
			}
			continue
		}

		// Slow path: decode full rune for non-ASCII
		r, size := utf8.DecodeRuneInString(key[i:])
		// If rune is in the control character range:
		// ASCII: r < 0x20 already handled in byte loop,
		// Extended control: U+007F-U+009F.
		if r >= 0x7F && r <= 0x9F {
			return fmt.Errorf("key contains forbidden control rune %U at byte position %d", r, i)
		}
		// Advance by the size of the decoded rune
		i += size - 1
	}

	return nil
}
