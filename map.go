package persist

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/puzpuzpuz/xsync/v3"
)

type PersistMap[T any] struct {
	store     *Store        // underlying WAL store
	data      *xsync.Map    // in-memory map holding decoded values of type T
	prefix    string        // namespace prefix for keys (e.g. "mapName:")
	dirty     *xsync.Map    // set of dirty keys; value is struct{} as a dummy
	stopFlush chan struct{} // channel to signal stop of background flush
	wg        sync.WaitGroup
}

// PersistMap represents a thread-safe persistent key-value store with type-safe values.
// It maintains an in-memory map for fast access while ensuring durability through the WAL.
// All values are validated during loading by ensuring they can be unmarshalled into type T.
// The mapName parameter is used as a namespace: keys will be stored as "mapName:key" in the WAL.
func Map[T any](store *Store, mapName string) (*PersistMap[T], error) {
	pm := &PersistMap[T]{
		store:     store,
		data:      xsync.NewMap(), // Using xsync.Map instead of built-in map
		prefix:    mapName + ":",  // Using "mapName:" as prefix for keys
		dirty:     xsync.NewMap(), // Initialize dirty set
		stopFlush: make(chan struct{}),
	}

	// Load data from the WAL file with immediate validation
	if err := pm.load(); err != nil {
		return nil, err
	}

	// Start background goroutine that flushes dirty entries to the WAL
	pm.startBackgroundFlush()

	// Register this PersistMap instance in the Store registry
	store.persistMaps.Store(mapName, pm)

	return pm, nil
}

// startBackgroundFlush launches a goroutine that periodically flushes dirty keys to the WAL
func (pm *PersistMap[T]) startBackgroundFlush() {
	pm.wg.Add(1)
	go func() {
		ticker := time.NewTicker(SyncInterval)
		defer ticker.Stop()
		defer pm.wg.Done()
		for {
			select {
			case <-ticker.C:
				pm.Flush()
			case <-pm.stopFlush:
				return
			}
		}
	}()
}

// Persists all dirty keys to the WAL
func (pm *PersistMap[T]) Flush() {
	// Iterate over dirty keys in the set
	pm.dirty.Range(func(key string, _ interface{}) bool {
		namespacedKey := pm.prefix + key

		// Using Compute on dirty map to atomically perform WAL update and remove the dirty flag
		pm.dirty.Compute(key, func(oldValue interface{}, loaded bool) (interface{}, bool) {
			// Lock is taken for this key. Now we can read the in-memory value.
			if v, ok := pm.data.Load(key); ok {
				// Try persisting the current value in WAL
				if err := pm.store.Set(namespacedKey, v); err != nil {
					log.Println("go-persist: Background flush set failed for key:", key, "error:", err)
					// Return oldValue and false, so that the dirty flag is not removed.
					return oldValue, false
				}
			} else {
				// If the key is no longer in data, try to delete it from WAL.
				if err := pm.store.Delete(namespacedKey); err != nil {
					log.Println("go-persist: Background flush delete failed for key:", key, "error:", err)
					return oldValue, false
				}
			}
			// WAL update succeeded; return nil and true to delete the dirty flag.
			return nil, true
		})

		return true
	})
}

// load reconstructs the in-memory map by replaying all records from the storage file.
// It processes only those records whose key starts with the map's prefix.
func (pm *PersistMap[T]) load() error {
	// Open file for reading from the beginning
	f, err := os.Open(pm.store.path)
	if err != nil {
		return err
	}
	defer f.Close()

	reader := bufio.NewReader(f)

	// Read and validate WAL header line
	headerLine, err := reader.ReadString('\n')
	if err != nil {
		return errors.New("WAL file is empty, missing header")
	}
	if strings.TrimSpace(headerLine) != WalHeader {
		return errors.New("invalid WAL header")
	}

	// Process records
	for {
		op, fullKey, valueLine, err := readRecord(reader)
		if err != nil {
			if err == io.EOF {
				break // End of file reached
			}
			return err
		}

		// Process only records that belong to this PersistMap (i.e. key starts with pm.prefix)
		if !strings.HasPrefix(fullKey, pm.prefix) {
			continue
		}
		// Remove prefix from key before storing in the in-memory map
		key := strings.TrimPrefix(fullKey, pm.prefix)

		if op == "S" {
			var v T
			// Unmarshal the JSON value into type T for validation
			if err := json.Unmarshal([]byte(valueLine), &v); err != nil {
				return err
			}
			pm.data.Store(key, v)
		} else if op == "D" {
			pm.data.Delete(key)
		}
	}

	return nil
}

// Get retrieves the value associated with the key from the in-memory map
func (pm *PersistMap[T]) Get(key string) (T, bool) {
	value, ok := pm.data.Load(key)
	if !ok {
		var zero T
		return zero, false
	}
	// Type assertion to type T
	typedValue, ok := value.(T)
	if !ok {
		panic("type assertion failed")
	}
	return typedValue, true
}

// Set updates the in-memory map and marks the key as dirty for background WAL flush
func (pm *PersistMap[T]) Set(key string, value T) {
	// Update in-memory xsync.Map
	pm.data.Store(key, value)
	// Mark key as dirty
	pm.dirty.Store(key, struct{}{})
}

// Delete removes the key from the in-memory map and marks it as dirty for background flush
func (pm *PersistMap[T]) Delete(key string) {
	pm.data.Delete(key)
	pm.dirty.Store(key, struct{}{})
}

// close stops the background flush goroutine
func (pm *PersistMap[T]) close() {
	pm.Flush()
	close(pm.stopFlush)
	pm.wg.Wait()
}
