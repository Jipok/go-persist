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
	mu            sync.Mutex     // protects concurrent access to the file
	f             *os.File       // file descriptor for append operations
	path          string         // file path used for reopening during reads
	stopSync      chan struct{}  // channel to signal background sync to stop
	wg            sync.WaitGroup // waitgroup for background sync goroutine
	persistMaps   *xsync.Map     // registry of PersistMap instances
	closedMaps    *xsync.Map     // list of map with was Close()
	orphanRecords *xsync.Map     // stores records that do not belong to any registered map
	syncInterval  atomic.Int64   // sync and flush interval background f.Sync() (representing a time.Duration)
	loaded        bool
	ErrorHandler  func(err error)
}

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
	return s.f.Close()
}

// FSyncAll ensures complete data durability by:
//
// 1. Synchronizing all dirty map entries to the WAL file
//
// 2. Performing an fsync operation to guarantee data is physically written to disk
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

// Write persists a key-value pair by writing a "set" record to the log.
// The record format consists of two lines:
// 1. S <key>
// 2. <json-serialized-value>
// The newline after the value serves as a marker that the record was
// successfully written and can be safely processed during recovery.
func (s *Store) Write(key string, value interface{}) error {
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
	return nil
}

// Delete marks a key as deleted by writing a "delete" record to the log.
// The record format consists of two lines:
// 1. D <key>
// 2. <Empty value line>
// The newline after the empty value line serves as a marker that the delete
// record was successfully written and can be safely processed during recovery.
func (s *Store) Delete(key string) error {
	if !s.loaded {
		return ErrNotLoaded
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	header := "D " + key + "\n"
	line := "\n"

	if _, err := s.f.Write([]byte(header + line)); err != nil {
		return err
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

// Read retrieves the most recent value for a key by scanning the entire log file.
// It processes all records sequentially, tracking whether the key was set or deleted.
// The target parameter must be a pointer where the unmarshalled JSON value will be stored.
// Returns ErrKeyNotFound if the key doesn't exist or was deleted in the most recent operation.
func (s *Store) Read(key string, target interface{}) error {
	// Lock for consistency (we use a separate file descriptor for reading)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		return ErrNotLoaded
	}

	// Open file for reading from the beginning
	f, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer f.Close()

	reader := bufio.NewReader(f)

	// Read and validate WAL header line ensuring it's properly terminated
	headerLine, err := reader.ReadString('\n')
	if err != nil {
		return errors.New("WAL file is empty, missing header")
	}
	if strings.TrimSpace(headerLine) != WalHeader {
		return errors.New("invalid WAL header")
	}

	var latestValue string
	found := false

	// Process WAL records one-by-one using the helper function
	for {
		op, recordKey, valueLine, err := readRecord(reader)
		if err != nil {
			if err == io.EOF {
				break // End of file reached
			}
			log.Println("go-persist: error reading record:", err)
			break
		}

		// Process the record only if key matches
		if recordKey != key {
			continue
		}

		if op == "S" {
			// Set/update operation – store the latest JSON value for the key
			latestValue = valueLine
			found = true
		} else if op == "D" {
			// Delete operation: mark the key as deleted
			found = false
		}
	}

	if !found {
		return ErrKeyNotFound
	}

	// Unmarshal the JSON value into target
	return json.Unmarshal([]byte(latestValue), target)
}

// Shrink compacts the WAL file by discarding deleted records and
// retaining only the latest set record for each key. The operation
// creates a new temporary file with the compacted data and then
// atomically replaces the original file. This helps to reclaim
// disk space and improve read performance.
func (s *Store) Shrink() error {
	// TODO Need less lock time

	// IMPORTANT REMINDER: When extending this code, always ensure that locks are acquired in a consistent order.
	// For example, any locks on xsync.Map in map Set/Update/Delete should be obtained before acquiring s.mu,
	// and avoid reversing this order. Violating this can lead to DEADLOCKS
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		return ErrNotLoaded
	}

	// Ensure all writes are flushed to disk
	if err := s.f.Sync(); err != nil {
		return err
	}

	// Open the existing WAL file for reading
	src, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer src.Close()

	reader := bufio.NewReader(src)

	// Validate WAL header
	headerLine, err := reader.ReadString('\n')
	if err != nil {
		return errors.New("WAL file is empty, missing header")
	}
	if strings.TrimSpace(headerLine) != WalHeader {
		return errors.New("invalid WAL header")
	}

	// Build a map to hold the latest set record for each key
	state := make(map[string]string)
	for {
		// Use helper function to read one record
		op, key, value, err := readRecord(reader)
		if err != nil {
			if err == io.EOF {
				break // End of file reached
			}
			log.Println("go-persist: error reading record during shrink:", err)
			break
		}

		switch op {
		case "S":
			// Update state with set operation value
			state[key] = value
		case "D":
			// Remove key from the state on delete operation
			delete(state, key)
		default:
			// Log a warning for unknown operations
			log.Println("go-persist: unknown operation encountered during shrink:", op)
		}
	}

	// Create a temporary file in the same directory for the compacted WAL
	tmpPath := s.path + ".tmp"
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	// Ensure temporary file is removed on error
	defer func() {
		tmpFile.Close()
		os.Remove(tmpPath)
	}()

	// Write the WAL header to the temporary file
	if _, err := tmpFile.WriteString(WalHeader + "\n"); err != nil {
		return err
	}

	// Write one set record per key from the state map
	for key, value := range state {
		// Write record header: "S <key>\n"
		if _, err := tmpFile.WriteString("S " + key + "\n"); err != nil {
			return err
		}
		// Write JSON value line followed by newline
		if _, err := tmpFile.WriteString(value + "\n"); err != nil {
			return err
		}
	}

	// Flush all writes to disk
	if err := tmpFile.Sync(); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	// Close the current file to allow replacing it (especially important on Windows)
	if err := s.f.Close(); err != nil {
		return err
	}

	// Atomically replace the original WAL file with the compacted temporary file
	if err := os.Rename(tmpPath, s.path); err != nil {
		return err
	}

	// Reopen the newly compacted WAL file for appending new records
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
