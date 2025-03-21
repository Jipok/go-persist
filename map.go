package persist

import (
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/puzpuzpuz/xsync/v3"
)

// persistMapI defines the common interface during bulk loading and Shrink()
type persistMapI interface {
	processRecord(op, fullKey, valueLine string) error
	writeRecords(w io.Writer) (int32, error)
}

type PersistMap[T any] struct {
	Store  *Store     // underlying WAL store
	data   *xsync.Map // in-memory map holding decoded values of type T
	prefix string     // namespace prefix for keys (e.g. "mapName:")
	dirty  *xsync.Map // set of dirty keys; value is struct{} as a dummy
}

var (
	ErrMapAlreadyExists = errors.New("persist map with the given name already exists in store")
	ErrTooLate          = errors.New("cannot register new map after store has been loaded")
)

// OpenSingleMap is the simplest way to get started with a persistent map when you need just one map per file.
// It opens the store, compacts the WAL, and initializes the map in a single operation.
//
// It returns a PersistMap that represents a thread-safe persistent key-value store with type-safe values of type T.
// This map maintains an in-memory representation for fast access while ensuring durability through the WAL.
func OpenSingleMap[T any](path string) (*PersistMap[T], error) {
	store := New()

	// Create a map with an empty namespace
	pm, err := Map[T](store, "")
	if err != nil {
		return nil, err
	}

	// Load records
	err = store.Open(path)
	if err != nil {
		return nil, err
	}

	// Periodically optimize storage
	store.StartAutoShrink(time.Minute, 1.8)

	return pm, nil
}

// Map creates or loads PersistMap from store.
//
// PersistMap represents a thread-safe persistent key-value store with type-safe values.
// It maintains an in-memory map for fast access while ensuring durability through the WAL.
//
// The mapName parameter is used as a namespace: keys will be stored as "mapName:key" in the WAL.
func Map[T any](store *Store, mapName string) (*PersistMap[T], error) {
	if err := ValidateKey(mapName); err != nil {
		return nil, err
	}

	_, closed := store.closedMaps.Load(mapName)
	if closed {
		return nil, errors.New("cannot reuse a map that was previously closed")
	}

	_, found := store.persistMaps.Load(mapName)
	if found {
		return nil, ErrMapAlreadyExists
	}

	pm := &PersistMap[T]{
		Store:  store,
		data:   xsync.NewMap(), // Using xsync.Map instead of built-in map
		prefix: mapName + ":",  // Using "mapName:" as prefix for keys
		dirty:  xsync.NewMap(), // Initialize dirty set
	}

	// Register this PersistMap instance in the Store registry
	store.persistMaps.Store(mapName, pm)

	// If the store is already loaded, process any orphan records for this map
	var err error
	if store.loaded {
		store.orphanRecords.Range(func(key string, value interface{}) bool {
			// Check if orphan key belongs to this map namespace
			if strings.HasPrefix(key, pm.prefix) {
				realKey := key[len(pm.prefix):]
				// Process orphan record as a "set" record
				if innerErr := pm.processRecord("S", realKey, value.(string)); innerErr != nil {
					err = fmt.Errorf("error processing orphan record for key `%s`: %s", key, innerErr)
					return false
				}
				// Delete processed orphan record
				store.orphanRecords.Delete(key)
			}
			return true
		})
	}

	return pm, err
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
				if err := pm.Store.write(namespacedKey, v); err != nil {
					log.Println("go-persist: Background flush set failed for key:", key, "error:", err)
					// Return oldValue and false, so that the dirty flag is not removed
					return oldValue, false
				}
			} else {
				// If the key is no longer in data, try to delete it from WAL
				if err := pm.Store.Delete(namespacedKey); err != nil {
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

// processRecord applies a record from the WAL to the in-memory map
func (pm *PersistMap[T]) processRecord(op, key, value string) error {
	switch op {
	case "S":
		var v T
		if err := json.Unmarshal([]byte(value), &v); err != nil {
			return err
		}
		pm.data.Store(key, v)
	case "D":
		pm.data.Delete(key)
	}
	return nil
}

// writeRecords writes all the in-memory records of the PersistMap to the provided writer.
// Each record is written as a "set" record in the WAL format.
// Need for Shrink()
func (pm *PersistMap[T]) writeRecords(w io.Writer) (int32, error) {
	var err error
	var counter int32 = 0
	pm.data.Range(func(key string, value interface{}) bool {
		data, e := json.Marshal(value)
		if e != nil {
			err = e
			return false
		}
		// The record format consists of two lines:
		// 1. S <key>
		// 2. <json-serialized-value>
		// The newline after the value serves as a marker that the record was
		// successfully written and can be safely processed during recovery.
		//
		// Full key is composed of pm.prefix "mapName:" plus the key
		header := "S " + pm.prefix + key + "\n"
		line := string(data) + "\n"
		if _, e = w.Write([]byte(header + line)); e != nil {
			err = e
			return false
		}
		counter++
		return true
	})
	return counter, err
}

/////////////////////////////////////////////////////////////////////////////////////////

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

// SetInMemory updates the value in memory only without explicitly writing to WAL
// or marking the key as dirty. This change won't trigger immediate persistence,
// but it may be persisted if:
//
// - This key is later modified with Set/Update/etc.
//
// - A Shrink operation occurs
//
// Useful for non-exported, derived, or cached fields.
func (pm *PersistMap[T]) SetInMemory(key string, value T) {
	pm.data.Store(key, value)
}

// UpdateInMemory atomically updates a value in memory only without writing to WAL
// or marking the key as dirty. This change won't trigger immediate persistence,
// but will be persisted if:
//
// - This key is later modified with Set/Update/etc.
//
// - A Shrink operation occurs
//
// The method uses the same Update struct as regular Update methods for API consistency,
// but only supports the Set action. Attempts to use Delete or Cancel will cause panic.
//
// Useful for non-exported, derived, or cached fields.
func (pm *PersistMap[T]) UpdateInMemory(key string, updater func(upd *Update[T])) T {
	newValIface, _ := pm.data.Compute(key, func(oldValue interface{}, loaded bool) (interface{}, bool) {
		var current T
		if loaded {
			current = oldValue.(T)
		}
		upd := &Update[T]{
			Value:  current,
			Exists: loaded,
			action: actionSet,
		}
		updater(upd)
		// Only the Set action is allowed
		if upd.action != actionSet {
			panic("Unsupported action in UpdateInMemory")
		}
		return upd.Value, false
	})
	return newValIface.(T)
}

/////////////////////////////////////////////////////////////////////////////////////////

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
		if err := pm.Store.write(namespacedKey, value); err != nil {
			pm.Store.ErrorHandler(err)
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
	// Flush (fsync) to ensure durability
	pm.Store.mu.Lock()
	defer pm.Store.mu.Unlock()
	return pm.Store.f.Sync()
}

// DeleteAsync removes the key from the in-memory map and marks it as dirty for background flush
// Returns true if the key existed and was deleted
func (pm *PersistMap[T]) DeleteAsync(key string) (existed bool) {
	// Remove the key from the in-memory xsync.Map
	pm.data.Compute(key, func(value interface{}, loaded bool) (interface{}, bool) {
		existed = loaded
		return value, true
	})
	// Mark the key as dirty
	pm.dirty.Store(key, struct{}{})
	return
}

// Delete immediately deletes the key from both WAL and in-memory map
// Returns true if the key existed and was deleted
func (pm *PersistMap[T]) Delete(key string) (existed bool) {
	pm.data.Compute(key, func(oldValue interface{}, loaded bool) (newValue interface{}, delete bool) {
		existed = loaded
		namespacedKey := pm.prefix + key
		// Write D record to disk(page cache) immediately
		if err := pm.Store.Delete(namespacedKey); err != nil {
			pm.Store.ErrorHandler(err)
		}
		// Remove the key from the in-memory xsync.Map
		return oldValue, true
	})
	return
}

// DeleteFSync writes a delete record to WAL immediately, flushes to disk (fsync),
// and updates the in-memory map.
func (pm *PersistMap[T]) DeleteFSync(key string) error {
	if !pm.Delete(key) {
		return ErrKeyNotFound
	}
	// Flush (fsync) to ensure durability
	pm.Store.mu.Lock()
	defer pm.Store.mu.Unlock()
	return pm.Store.f.Sync()
}

/////////////////////////////////////////////////////////////////////////////////////////

// Update encapsulates the state for updating a key in the map.
// It holds the current value and its existence flag, and allows the updater
// to specify the intended action: update (set), deletion, or cancellation.
// By default, the action is set to update, so modifying the Value field directly
// implies a "set" operation.
type Update[T any] struct {
	Value  T          // Current value retrieved from the map
	Exists bool       // Whether the key exists
	action actionType // The chosen action
}

type actionType int

const (
	actionNone   actionType = iota // No action was taken
	actionSet                      // Update the value (default operation)
	actionDelete                   // Delete the key
	actionCancel                   // Cancel the update
)

// Set updates the internal value and marks the action as "set".
// Note that simply modifying the Value field directly also works
// since "set" is the default action.
func (ua *Update[T]) Set(newVal T) {
	ua.Value = newVal
	ua.action = actionSet
}

// Delete indicates that the key should be deleted
func (ua *Update[T]) Delete() {
	ua.action = actionDelete
}

// Cancel indicates that no changes should be applied
func (ua *Update[T]) Cancel() {
	ua.action = actionCancel
}

// UpdateAsync atomically updates a key using the updater function.
//
// It only updates the in-memory value and marks the key as dirty so that background FSyncAll
// will eventually persist the change.
//
// The updater receives an UpdateAction containing the current value and existence state. It can:
//
// - Simply modify upd.Value to update the value (default action is "set")
//
// - Call upd.Set() to explicitly set a new value
//
// - Call upd.Delete() to remove the key
//
// - Call upd.Cancel() to keep the original value unchanged
//
// This method locks the relevant hash table bucket during execution, so avoid long-running
// operations in the updater function to prevent blocking other bucket operations.
func (pm *PersistMap[T]) UpdateAsync(key string, updater func(upd *Update[T])) (newValue T, exists bool) {
	newValIface, ok := pm.data.Compute(key, func(oldValue interface{}, loaded bool) (interface{}, bool) {
		var current T
		if loaded {
			current = oldValue.(T)
		}
		upd := &Update[T]{
			Value:  current,
			Exists: loaded,
			action: actionSet,
		}
		updater(upd)

		switch upd.action {
		case actionDelete:
			// Mark key for deletion (Compute returns delete flag)
			return nil, true
		case actionSet:
			// Set new value
			return upd.Value, false
		default:
			// If cancelled, return the original value and state
			return oldValue, !loaded
		}
	})
	// Mark the key as dirty for asynchronous persistence
	pm.dirty.Store(key, struct{}{})

	if !ok {
		var zero T
		return zero, false
	}
	return newValIface.(T), true
}

// Update atomically updates the value for the given key using the updater function,
// and immediately writes the change to the WAL (without forcing disk fsync).
//
// The updater receives an UpdateAction containing the current value and existence state. It can:
//
// - Simply modify upd.Value to update the value (default action is "set")
//
// - Call upd.Set() to explicitly set a new value
//
// - Call upd.Delete() to remove the key
//
// - Call upd.Cancel() to keep the original value unchanged
//
// This method locks the relevant hash table bucket during execution, so avoid long-running
// operations in the updater function to prevent blocking other bucket operations.
func (pm *PersistMap[T]) Update(key string, updater func(upd *Update[T])) (newValue T, exists bool) {
	newValIface, ok := pm.data.Compute(key, func(oldValue interface{}, loaded bool) (interface{}, bool) {
		var current T
		if loaded {
			current = oldValue.(T)
		}
		upd := &Update[T]{
			Value:  current,
			Exists: loaded,
			action: actionSet,
		}
		updater(upd)
		namespacedKey := pm.prefix + key

		switch upd.action {
		case actionDelete:
			// Write D record atomically inside Compute callback
			if err := pm.Store.Delete(namespacedKey); err != nil {
				pm.Store.ErrorHandler(err)
			}
			// Returning true signals removal of the key from the map
			return nil, true
		case actionSet:
			// Write S record atomically inside Compute callback
			if err := pm.Store.write(namespacedKey, upd.Value); err != nil {
				pm.Store.ErrorHandler(err)
			}
			// Returning false signals that the key should be kept in the map
			return upd.Value, false
		default:
			// If cancelled, return the original value and state
			return oldValue, !loaded
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
// The updater receives an UpdateAction containing the current value and existence state. It can:
//
// - Simply modify upd.Value to update the value (default action is "set")
//
// - Call upd.Set() to explicitly set a new value
//
// - Call upd.Delete() to remove the key
//
// - Call upd.Cancel() to keep the original value unchanged
//
// This method locks the relevant hash table bucket during execution, so avoid long-running
// operations in the updater function to prevent blocking other bucket operations.
func (pm *PersistMap[T]) UpdateFSync(key string, updater func(upd *Update[T])) (newValue T, exists bool, err error) {
	newValue, exists = pm.Update(key, updater)
	// Flush (fsync) to ensure durability
	pm.Store.mu.Lock()
	defer pm.Store.mu.Unlock()
	err = pm.Store.f.Sync()
	return
}

/////////////////////////////////////////////////////////////////////////////////////////

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
