package persist

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
)

// Store represents the WAL(write-ahead log) storage
type Store struct {
	mu           sync.Mutex     // protects concurrent access to the file
	f            *os.File       // file descriptor for append operations
	path         string         // file path used for reopening during reads
	stopSync     chan struct{}  // channel to signal background sync to stop
	wg           sync.WaitGroup // waitgroup for background sync goroutine
	persistMaps  *xsync.Map     // registry of PersistMap instances (values are Closer interface)
	syncInterval atomic.Int64   // sync and flush interval background f.Sync() (representing a time.Duration)
	ErrorHandler func(err error)
}

// Open creates or opens a persistent storage file and returns a new Store instance
func Open(path string) (*Store, error) {
	// Open the file in read/write and append mode (create if not exist)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	// Validate or write WAL header
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}

	if stat.Size() == 0 {
		// File is new, write header
		if _, err := f.Write([]byte(WalHeader + "\n")); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, err
		}
	} else {
		// Validate existing header
		// Seek to the beginning to read the header line
		if _, err := f.Seek(0, 0); err != nil {
			f.Close()
			return nil, err
		}
		reader := bufio.NewReader(f)
		headerLine, err := reader.ReadString('\n')
		if err != nil {
			f.Close()
			return nil, err
		}
		if strings.TrimSpace(headerLine) != WalHeader {
			f.Close()
			return nil, errors.New("invalid WAL header, unsupported WAL file")
		}
		// Seek back to the end for appending writes
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			f.Close()
			return nil, err
		}
	}

	s := &Store{
		f:           f,
		path:        path,
		stopSync:    make(chan struct{}),
		persistMaps: xsync.NewMap(),
	}
	s.SetSyncInterval(DefaultSyncInterval)

	s.ErrorHandler = func(err error) {
		log.Fatal("go-persist: ", err)
	}

	// Start background FSyncAll
	s.wg.Add(1)
	go func() {
		timer := time.NewTimer(s.GetSyncInterval())
		defer timer.Stop()
		defer s.wg.Done()
		for {
			select {
			case <-timer.C:
				err := s.FSyncAll()
				if err != nil {
					s.ErrorHandler(fmt.Errorf("background sync failed: %s", err))
				}
				timer.Reset(s.GetSyncInterval())
			case <-s.stopSync:
				return
			}
		}
	}()

	return s, nil
}

// Saves all pending changes and stops the background flush goroutine
// Then closes the underlying file.
//
// The Store should not be used after calling Close.
func (s *Store) Close() error {
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
	// Flush Maps
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
	headerLine, err := reader.ReadString('\n')
	if err != nil {
		return "", "", "", err
	}
	headerLine = strings.TrimSuffix(headerLine, "\n")
	if strings.TrimSpace(headerLine) == "" {
		return "", "", "", errors.New("empty record header")
	}
	parts := strings.SplitN(headerLine, " ", 2)
	if len(parts) != 2 {
		return "", "", "", errors.New("invalid record header format")
	}
	op, key = parts[0], parts[1]

	// Read value line (ensure it ends with a newline)
	valueLine, err := reader.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			log.Printf("go-persist: incomplete record detected, reached EOF after header: %q, partial value: %q", headerLine, valueLine)
		}
		return "", "", "", err
	}
	value = strings.TrimSuffix(valueLine, "\n")

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
