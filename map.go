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

// Map creates or loads PersistMap from store.
//
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
			// Lock is taken for this key. Now we can read the in-memory value
			if v, ok := pm.data.Load(key); ok {
				// Try persisting the current value in WAL
				if err := pm.store.Write(namespacedKey, v); err != nil {
					log.Println("go-persist: Background flush set failed for key:", key, "error:", err)
					// Return oldValue and false, so that the dirty flag is not removed
					return oldValue, false
				}
			} else {
				// If the key is no longer in data, try to delete it from WAL
				if err := pm.store.Delete(namespacedKey); err != nil {
					log.Println("go-persist: Background flush delete failed for key:", key, "error:", err)
					return oldValue, false
				}
			}
			// WAL update succeeded; return nil and true to delete the dirty flag
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

// Get retrieves the value associated with the key from the in-memory map.
//
// Returns the value and true if the key exists, or a zero value and false otherwise.
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
//
// Its actual persistence is deferred to a background flush, providing higher performance
// at the cost of delayed durability.
func (pm *PersistMap[T]) SetAsync(key string, value T) {
	// Update in-memory xsync.Map
	pm.data.Store(key, value)
	// Mark key as dirty
	pm.dirty.Store(key, struct{}{}) // Faster than LoadOrStore
}

// Set updates both in-memory data and WAL file immediately, but without fsync.
//
// Safe for application crashes, as WAL ensures recovery, but may lose updates
// during system crashes if data remains in OS cache.
func (pm *PersistMap[T]) Set(key string, value T) {
	pm.data.Compute(key, func(oldValue interface{}, loaded bool) (newValue interface{}, delete bool) {
		namespacedKey := pm.prefix + key
		// Write S record to disk(page cache) immediately
		if err := pm.store.Write(namespacedKey, value); err != nil {
			pm.store.ErrorHandler(err)
		}
		// Update in-memory xsync.Map
		return value, false
	})
}

// SetFSync updates in-memory data, WAL file, and forces physical disk write with fsync.
//
// Most durable option that protects against both application and system crashes,
// but with highest performance cost.
func (pm *PersistMap[T]) SetFSync(key string, value T) error {
	pm.Set(key, value)
	// Flush all pending writes to disk (fsync)
	return pm.store.FSyncAll()
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
	pm.data.Compute(key, func(oldValue interface{}, loaded bool) (newValue interface{}, delete bool) {
		namespacedKey := pm.prefix + key
		// Write D record to disk(page cache) immediately
		if err := pm.store.Delete(namespacedKey); err != nil {
			pm.store.ErrorHandler(err)
		}
		// Remove the key from the in-memory xsync.Map
		return oldValue, true
	})
}

// DeleteFSync writes a delete record to WAL immediately, flushes to disk (fsync),
// and updates the in-memory map.
func (pm *PersistMap[T]) DeleteFSync(key string) error {
	pm.Delete(key)
	// Flush all pending writes to disk (fsync)
	return pm.store.FSyncAll()
}

// UpdateAsync atomically updates a key using the updater function.
//
// It only updates the in-memory value and marks the key as dirty so that background FSyncAll
// will eventually persist the change.
//
// The updater function receives current value (or zero value if not exists) and a flag indicating its existence,
// and should return the new value and a flag indicating whether to delete the key.
//
// This call locks a hash table bucket while the updater function
// is executed. It means that modifications on other entries in
// the bucket will be blocked until the updater executes. Consider
// this when the function includes long-running operations.
func (pm *PersistMap[T]) UpdateAsync(key string, updater func(current T, exists bool) (newValue T, delete bool)) (newValue T, existed bool) {
	// Atomically update the in-memory map
	newValIface, ok := pm.data.Compute(key, func(oldValue interface{}, loaded bool) (interface{}, bool) {
		var current T
		if loaded {
			current = oldValue.(T)
		}
		// updater returns (newValue, delete)
		return updater(current, loaded)
	})
	// Always mark the key as dirty for eventual WAL persistence
	pm.dirty.Store(key, struct{}{})

	// If Compute indicates deletion, return zero value and false
	if !ok {
		var zero T
		return zero, false
	}
	return newValIface.(T), true
}

// Update atomically updates the value for the given key using the updater function,
// and immediately writes the change to the WAL (without forcing disk fsync).
//
// The updater function receives current value (or zero value if not exists) and a flag indicating its existence,
// and should return the new value and a flag indicating whether to delete the key.
//
// This call locks a hash table bucket while the updater function
// is executed. It means that modifications on other entries in
// the bucket will be blocked until the updater executes. Consider
// this when the function includes long-running operations.
func (pm *PersistMap[T]) Update(key string, updater func(current T, exists bool) (newValue T, doDelete bool)) (newValue T, existed bool) {
	newValIface, ok := pm.data.Compute(key, func(oldValue interface{}, loaded bool) (interface{}, bool) {
		var current T
		if loaded {
			current = oldValue.(T)
		}
		result, doDelete := updater(current, loaded)
		namespacedKey := pm.prefix + key
		if doDelete {
			// Write D record atomically inside Compute callback
			if err := pm.store.Delete(namespacedKey); err != nil {
				pm.store.ErrorHandler(err)
			}
			// Returning true signals removal of the key from the map
			return nil, true
		} else {
			// Write S record atomically inside Compute callback
			if err := pm.store.Write(namespacedKey, result); err != nil {
				pm.store.ErrorHandler(err)
			}
			// Returning false signals that the key should be kept in the map
			return result, false
		}
	})
	if !ok {
		var zero T
		return zero, false
	}
	return newValIface.(T), true
}

// UpdateFSync atomically updates a key using the updater function, writes to the WAL, and forces a physical disk flush (fsync).
//
// Offers maximum durability against both application and system crashes, but with the highest performance cost.
//
// The updater function receives current value (or zero value if not exists) and a flag indicating its existence,
// and should return the new value and a flag indicating whether to delete the key.
//
// This call locks a hash table bucket while the updater function
// is executed. It means that modifications on other entries in
// the bucket will be blocked until the updater executes. Consider
// this when the function includes long-running operations.
func (pm *PersistMap[T]) UpdateFSync(key string, updater func(current T, exists bool) (newValue T, delete bool)) (newValue T, existed bool, err error) {
	newValue, existed = pm.Update(key, updater)
	// Flush (fsync) to ensure durability
	err = pm.store.FSyncAll()
	return
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
