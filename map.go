package persist

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"strings"

	"github.com/puzpuzpuz/xsync/v3"
)

type PersistMap[T any] struct {
	store  *Store     // underlying WAL store
	data   *xsync.Map // in-memory map holding decoded values of type T
	prefix string     // namespace prefix for keys (e.g. "mapName:")
	dirty  *xsync.Map // set of dirty keys; value is struct{} as a dummy
}

// PersistMap represents a thread-safe persistent key-value store with type-safe values.
// It maintains an in-memory map for fast access while ensuring durability through the WAL.
// All values are validated during loading by ensuring they can be unmarshalled into type T.
// The mapName parameter is used as a namespace: keys will be stored as "mapName:key" in the WAL.
func Map[T any](store *Store, mapName string) (*PersistMap[T], error) {
	pm := &PersistMap[T]{
		store:  store,
		data:   xsync.NewMap(), // Using xsync.Map instead of built-in map
		prefix: mapName + ":",  // Using "mapName:" as prefix for keys
		dirty:  xsync.NewMap(), // Initialize dirty set
	}

	// Load data from the WAL file with immediate validation
	if err := pm.load(); err != nil {
		return nil, err
	}

	// Register this PersistMap instance in the Store registry
	store.persistMaps.Store(mapName, pm)

	return pm, nil
}

// Sync writes all pending changes made by Async methods to the WAL file.
// It processes all keys marked as dirty, persisting their current values or
// deletion status, then clears their dirty flags upon successful write.
//
// Sync is automatically called periodically from the store's background
// synchronization process. This method only ensures consistency between
// memory and the WAL file, but doesn't guarantee data is physically
// written to disk - that step is handled by the Store.Flush method.
func (pm *PersistMap[T]) Sync() {
	// Iterate over dirty keys in the set
	pm.dirty.Range(func(key string, _ interface{}) bool {
		namespacedKey := pm.prefix + key

		pm.dirty.Compute(key, func(oldValue interface{}, loaded bool) (interface{}, bool) {
			// Lock is taken for this key. Now we can read the in-memory value.
			if v, ok := pm.data.Load(key); ok {
				// Try persisting the current value in WAL
				if err := pm.store.Write(namespacedKey, v); err != nil {
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

// SetAsync updates the in-memory map and marks the key as dirty.
// Its actual persistence is deferred to a background flush, providing higher performance
// at the cost of delayed durability.
func (pm *PersistMap[T]) SetAsync(key string, value T) {
	// Update in-memory xsync.Map
	pm.data.Store(key, value)
	// Mark key as dirty
	pm.dirty.Store(key, struct{}{}) // Faster than LoadOrStore
}

// Set updates both in-memory data and WAL file immediately, but without fsync.
// Safe for application crashes, as WAL ensures recovery, but may lose updates
// during system crashes if data remains in OS cache.
func (pm *PersistMap[T]) Set(key string, value T) {
	namespacedKey := pm.prefix + key
	// Write the set record to disk(page cache) immediately
	if err := pm.store.Write(namespacedKey, value); err != nil {
		pm.store.ErrorHandler(err)
	}
	// Update in-memory xsync.Map
	pm.data.Store(key, value)
}

// SetFSync updates in-memory data, WAL file, and forces physical disk write with fsync.
// Most durable option that protects against both application and system crashes,
// but with highest performance cost.
func (pm *PersistMap[T]) SetFSync(key string, value T) error {
	namespacedKey := pm.prefix + key
	// Write the set record to disk immediately
	if err := pm.store.Write(namespacedKey, value); err != nil {
		return err
	}
	// Flush all pending writes to disk (fsync)
	if err := pm.store.FSyncAll(); err != nil {
		return err
	}
	// Update in-memory xsync.Map
	pm.data.Store(key, value)
	return nil
}

// DeleteAsync removes the key from the in-memory map and marks it as dirty for background flush
func (pm *PersistMap[T]) DeleteAsync(key string) {
	// Remove the key from the in-memory xsync.Map
	pm.data.Delete(key)
	// Mark the key as dirty
	pm.dirty.Store(key, struct{}{})
}

// Delete immediately deletes the key from both WAL and in-memory map
func (pm *PersistMap[T]) Delete(key string) {
	namespacedKey := pm.prefix + key
	// Write the delete record to WAL immediately
	if err := pm.store.Delete(namespacedKey); err != nil {
		pm.store.ErrorHandler(err)
	}
	// Remove the key from the in-memory xsync.Map
	pm.data.Delete(key)
}

// DeleteFSync writes a delete record to WAL immediately, flushes to disk (fsync),
// and updates the in-memory map.
func (pm *PersistMap[T]) DeleteFSync(key string) error {
	namespacedKey := pm.prefix + key
	// Write the delete record to WAL immediately
	if err := pm.store.Delete(namespacedKey); err != nil {
		return err
	}
	// Flush all pending writes to disk (fsync)
	if err := pm.store.FSyncAll(); err != nil {
		return err
	}
	// Remove the key from the in-memory xsync.Map
	pm.data.Delete(key)
	return nil
}

// Size returns current size of the map
func (pm *PersistMap[T]) Size() int {
	return pm.data.Size()
}

// Range calls f sequentially for each key and value present in the
// map. If f returns false, range stops the iteration.
//
// Range does not necessarily correspond to any consistent snapshot
// of the Map's contents: no key will be visited more than once, but
// if the value for any key is stored or deleted concurrently, Range
// may reflect any mapping for that key from any point during the
// Range call.
//
// It is safe to modify the map while iterating it, including entry
// creation, modification and deletion. However, the concurrent
// modification rule apply, i.e. the changes may be not reflected
// in the subsequently iterated entries.
func (pm *PersistMap[T]) Range(f func(key string, value T) bool) {
	pm.data.Range(func(key string, value interface{}) bool {
		// Type assertion from interface{} to T
		return f(key, value.(T))
	})
}
