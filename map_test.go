package persist

import (
	"math/rand"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestPersistMap_SetGet tests basic Set/Get/Delete operations.
func TestPersistMap_SetGet(t *testing.T) {
	// Create a temporary file for the WAL.
	tempFile, err := os.CreateTemp("", "persist_map_test_*.wal")
	if err != nil {
		t.Fatal(err)
	}
	path := tempFile.Name()
	tempFile.Close() // Close the file, it will be reopened by NewStore.
	defer os.Remove(path)

	// Create a new WAL store.
	pm, err := OpenSingleMap[string](path)
	defer pm.Store.Close()

	// Set a value.
	pm.Set("foo", "bar") // Updated: no error returned
	// Get the value.
	val, ok := pm.Get("foo")
	if !ok {
		t.Fatalf("Get failed: key 'foo' not found")
	}
	if val != "bar" {
		t.Errorf("Expected 'bar', got %q", val)
	}

	// Delete the key.
	if !pm.DeleteAsync("foo") {
		t.Errorf("Expected key 'foo' to be deleted")
	}

	// Delete the wrong key
	if pm.Delete("not exist") {
		t.Errorf("Delete returns true for `not exist` key")
	}

	// Get should now not find the key.
	_, ok = pm.Get("foo")
	if ok {
		t.Errorf("Expected key 'foo' to be deleted, but it was found")
	}
}

// TestPersistMap_Complex performs concurrent operations (Set/Delete) with multiple goroutines,
// then freezes final state, closes and reopens the store to validate data consistency,
// calls Shrink, and again verifies that persisted data is correct.
func TestPersistMap_Complex(t *testing.T) {
	// Initialize random seed for reproducibility
	rand.Seed(42)

	// Create a temporary file for the WAL.
	tmpFile, err := os.CreateTemp("", "persist_map_complex_test_*.wal")
	if err != nil {
		t.Fatal(err)
	}
	path := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(path)

	// Create a new WAL store.
	pm, err := OpenSingleMap[int](path)
	if err != nil {
		t.Fatal(err)
	}

	// Launch multiple goroutines performing random Set/Delete operations.
	var wg sync.WaitGroup
	numGoroutines := 20
	iterations := 50

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				// Randomly select a key from key0 to key9.
				keyIdx := rand.Intn(10)
				key := "key" + strconv.Itoa(keyIdx)
				// Randomly choose operation: 0 for Set, 1 for Delete.
				if rand.Intn(2) == 0 {
					// Compute a value unique for this operation.
					val := id*1000 + j
					pm.Set(key, val) // Updated: no error returned
				} else {
					pm.Delete(key) // Updated: no error returned
				}
				// Small sleep to increase concurrency variability.
				time.Sleep(time.Microsecond)
			}
		}(i)
	}
	wg.Wait()

	// Freeze the final state:
	// For keys "key0" ... "key9", for even indices perform Set (with value equal to key index),
	// for odd indices perform Delete.
	for i := 0; i < 10; i++ {
		key := "key" + strconv.Itoa(i)
		if i%2 == 0 {
			pm.Set(key, i) // Updated: no error returned
		} else {
			pm.Delete(key) // Updated: no error returned
		}
	}

	// Verify final state in-memory.
	for i := 0; i < 10; i++ {
		key := "key" + strconv.Itoa(i)
		if i%2 == 0 {
			val, ok := pm.Get(key)
			if !ok {
				t.Fatalf("Final get failed: key %s not found", key)
			}
			if val != i {
				t.Errorf("Final value mismatch for key %s: expected %d, got %d", key, i, val)
			}
		} else {
			_, ok := pm.Get(key)
			if ok {
				t.Errorf("Expected key %s to be deleted, but it was found", key)
			}
		}
	}

	// Close the store to flush all operations.
	if err := pm.Store.Close(); err != nil {
		t.Fatalf("Store close failed: %v", err)
	}

	// ---------- Reopen and validate state from WAL ----------

	// Reopen store.
	store2 := New()
	err = store2.Open(path)
	if err != nil {
		t.Fatalf("Failed to reopen store: %v", err)
	}
	pmReloaded, err := Map[int](store2, "")
	if err != nil {
		t.Fatalf("Failed to reload persist map: %v", err)
	}

	// Verify state after reload.
	for i := 0; i < 10; i++ {
		key := "key" + strconv.Itoa(i)
		if i%2 == 0 {
			val, ok := pmReloaded.Get(key)
			if !ok {
				t.Fatalf("Reloaded get failed: key %s not found", key)
			}
			if val != i {
				t.Errorf("Reloaded value mismatch for key %s: expected %d, got %d", key, i, val)
			}
		} else {
			_, ok := pmReloaded.Get(key)
			if ok {
				t.Errorf("Reloaded expected key %s to be deleted, but it was found", key)
			}
		}
	}

	// Call shrink on the store.
	if err := store2.Shrink(); err != nil {
		t.Fatalf("Shrink failed: %v", err)
	}

	// Close store after shrink.
	if err := store2.Close(); err != nil {
		t.Fatalf("Store close after shrink failed: %v", err)
	}

	// ---------- Reopen once more after shrink and validate state ----------

	pmReloaded2, err := OpenSingleMap[int](path)
	if err != nil {
		t.Fatalf("Failed to reload persist map after shrink: %v", err)
	}
	defer pmReloaded2.Store.Close()

	// Final state verification after shrink.
	for i := 0; i < 10; i++ {
		key := "key" + strconv.Itoa(i)
		if i%2 == 0 {
			val, ok := pmReloaded2.Get(key)
			if !ok {
				t.Fatalf("Final reload get failed: key %s not found", key)
			}
			if val != i {
				t.Errorf("Final reload value mismatch for key %s: expected %d, got %d", key, i, val)
			}
		} else {
			_, ok := pmReloaded2.Get(key)
			if ok {
				t.Errorf("Final reload expected key %s to be deleted, but it was found", key)
			}
		}
	}
}

// TestPersistMap_MultipleMaps tests persistence with multiple maps belonging to different namespaces.
func TestPersistMap_MultipleMaps(t *testing.T) {
	// Create a temporary file for the WAL.
	tmpFile, err := os.CreateTemp("", "persist_map_multi_test_*.wal")
	if err != nil {
		t.Fatal(err)
	}
	walPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(walPath)

	// Open a new WAL store.
	store := New()
	err = store.Open(walPath)
	if err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}

	// Create first PersistMap[string] with empty namespace
	map1, err := Map[string](store, "first")
	if err != nil {
		t.Fatalf("Failed to create persist map 'first': %v", err)
	}

	// Create second PersistMap[int] with namespace "second".
	map2, err := Map[int](store, "second")
	if err != nil {
		t.Fatalf("Failed to create persist map 'second': %v", err)
	}

	// Perform operations on map1.
	map1.Set("", "hello")
	map1.Set("key2", "world")
	// Delete key2 from map1.
	map1.Delete("key2")

	// Perform operations on map2.
	map2.Set("one", 1)
	map2.Set("two", 2)
	// Delete key 'two' from map2.
	map2.Delete("two")
	map2.Delete("Unknown")

	// Close the store to flush all operations.
	if err := store.Close(); err != nil {
		t.Fatalf("Store close failed: %v", err)
	}

	// ---------- Reopen and validate state from WAL for multiple maps ----------

	store2 := New()

	// Reload the maps from the store. Pre-open
	reloadedMap1, err := Map[string](store2, "first")
	if err != nil {
		t.Fatalf("Failed to reload persist map 'first': %v", err)
	}

	err = store2.Open(walPath)
	if err != nil {
		t.Fatalf("Failed to reopen store: %v", err)
	}

	// After-open
	reloadedMap2, err := Map[int](store2, "second")
	if err != nil {
		t.Fatalf("Failed to reload persist map 'second': %v", err)
	}

	// Validate map1
	val, ok := reloadedMap1.Get("")
	if !ok {
		t.Fatalf("Failed to get empty key from map1")
	}
	if val != "hello" {
		t.Errorf("Expected 'hello' for key 'key1' in map1, got %q", val)
	}
	_, ok = reloadedMap1.Get("key2")
	if ok {
		t.Errorf("Expected key 'key2' to be deleted in map1, but it was found")
	}

	// Validate map2.
	val2, ok := reloadedMap2.Get("one")
	if !ok {
		t.Fatalf("Failed to get 'one' from map2")
	}
	if val2 != 1 {
		t.Errorf("Expected 1 for key 'one' in map2, got %d", val2)
	}
	_, ok = reloadedMap2.Get("two")
	if ok {
		t.Errorf("Expected key 'two' to be deleted in map2, but it was found")
	}

	// Close the reopened store.
	if err := store2.Close(); err != nil {
		t.Fatalf("Store close failed on reopened store: %v", err)
	}
}

// TestPersistMap_ComplexUpdateOperations performs concurrent Update/UpdateAsync operations,
// then freezes final state and verifies that the persisted state matches.
func TestPersistMap_ComplexUpdateOperations(t *testing.T) {
	// Create a temporary file for the WAL.
	tmpFile, err := os.CreateTemp("", "persist_map_complex_update_test_*.wal")
	if err != nil {
		t.Fatal(err)
	}
	path := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(path)

	// Open a new WAL store.
	store := New()
	err = store.Open(path)
	if err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}

	// Create a PersistMap[int] with empty namespace
	pm, err := Map[int](store, "")
	if err != nil {
		t.Fatalf("Failed to create persist map: %v", err)
	}

	// Define a set of keys to update.
	keys := []string{"counter0", "counter1", "counter2", "counter3", "counter4", "counter5", "counter6", "counter7", "counter8", "counter9"}

	var wg sync.WaitGroup
	numGoroutines := 5
	iterations := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				// Randomly choose one key.
				key := keys[rand.Intn(len(keys))]
				// Randomly choose to use Update (synchronous) or UpdateAsync.
				useSync := rand.Intn(2) == 0 // 50% chance.
				// Randomly decide whether to delete the key (20% chance).
				deletion := rand.Intn(100) < 20

				if useSync {
					// Using Update method.
					pm.Update(key, func(upd *Update[int]) {
						if deletion {
							upd.Delete()
							return
						}
						// If exists add a random delta (1..10); if not, initialize with a random value.
						delta := rand.Intn(10) + 1
						if upd.Exists {
							upd.Value += delta
							return
						}
						upd.Value = delta
					})
				} else {
					// Using UpdateAsync method.
					pm.UpdateAsync(key, func(upd *Update[int]) {
						if deletion {
							upd.Delete()
							return
						}
						delta := rand.Intn(10) + 1
						if upd.Exists {
							upd.Value += delta
							return
						}
						upd.Value = delta
					})
				}
				// Small sleep to increase concurrency variability.
				time.Sleep(time.Microsecond)
			}
		}(i)
	}
	wg.Wait()

	// Freeze final state: for each key, if its index is even, set it to a fixed value,
	// if odd, remove the key.
	for i, key := range keys {
		if i%2 == 0 {
			// Update key to have a known constant value (e.g. i * 100).
			pm.Update(key, func(upd *Update[int]) {
				upd.Value = i * 100
			})
		} else {
			// Force deletion of the key.
			pm.Update(key, func(upd *Update[int]) {
				upd.Delete()
			})
		}
	}

	// Verify in-memory final state.
	for i, key := range keys {
		if i%2 == 0 {
			// For even-indexed keys, the value should be exactly i*100.
			val, ok := pm.Get(key)
			if !ok {
				t.Fatalf("Final state: expected key %s to exist", key)
			}
			expected := i * 100
			if val != expected {
				t.Errorf("Final state: for key %s expected %d, got %d", key, expected, val)
			}
		} else {
			// For odd-indexed keys, the key should be deleted.
			if _, ok := pm.Get(key); ok {
				t.Errorf("Final state: expected key %s to be deleted", key)
			}
		}
	}

	// Close the store to flush all operations.
	if err := store.Close(); err != nil {
		t.Fatalf("Store close failed: %v", err)
	}

	// ---------- Reopen store and verify that persisted state matches ----------

	pmReloaded, err := OpenSingleMap[int](path)
	if err != nil {
		t.Fatalf("Failed to reload persist map: %v", err)
	}

	// Validate persisted state.
	for i, key := range keys {
		if i%2 == 0 {
			val, ok := pmReloaded.Get(key)
			if !ok {
				t.Fatalf("Reloaded state: expected key %s to exist", key)
			}
			expected := i * 100
			if val != expected {
				t.Errorf("Reloaded state: for key %s expected %d, got %d", key, expected, val)
			}
		} else {
			if _, ok := pmReloaded.Get(key); ok {
				t.Errorf("Reloaded state: expected key %s to be deleted", key)
			}
		}
	}
}
