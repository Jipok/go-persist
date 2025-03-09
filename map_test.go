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
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create a PersistMap[string] with namespace "test".
	pm, err := Map[string](store, "test")
	if err != nil {
		t.Fatalf("Failed to create persist map: %v", err)
	}

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
	pm.Delete("foo") // Updated: no error returned

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
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	// Create a PersistMap[int] with namespace "complex".
	pm, err := Map[int](store, "complex")
	if err != nil {
		t.Fatalf("Failed to create persist map: %v", err)
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
	if err := store.Close(); err != nil {
		t.Fatalf("Store close failed: %v", err)
	}

	// ---------- Reopen and validate state from WAL ----------

	// Reopen store.
	store2, err := Open(path)
	if err != nil {
		t.Fatalf("Failed to reopen store: %v", err)
	}
	// Note: we do not defer store2.Close() here because we'll close it explicitly.
	pmReloaded, err := Map[int](store2, "complex")
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

	store3, err := Open(path)
	if err != nil {
		t.Fatalf("Failed to reopen store after shrink: %v", err)
	}
	defer store3.Close()
	pmReloaded2, err := Map[int](store3, "complex")
	if err != nil {
		t.Fatalf("Failed to reload persist map after shrink: %v", err)
	}

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
	store, err := Open(walPath)
	if err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}

	// Create first PersistMap[string] with namespace "first".
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
	map1.Set("key1", "hello") // Updated: no error returned
	map1.Set("key2", "world") // Updated: no error returned
	// Delete key2 from map1.
	map1.Delete("key2") // Updated: no error returned

	// Perform operations on map2.
	map2.Set("one", 1) // Updated: no error returned
	map2.Set("two", 2) // Updated: no error returned
	// Delete key 'two' from map2.
	map2.Delete("two") // Updated: no error returned

	// Close the store to flush all operations.
	if err := store.Close(); err != nil {
		t.Fatalf("Store close failed: %v", err)
	}

	// ---------- Reopen and validate state from WAL for multiple maps ----------

	// Reopen the store.
	store2, err := Open(walPath)
	if err != nil {
		t.Fatalf("Failed to reopen store: %v", err)
	}

	// Reload the maps from the store.
	reloadedMap1, err := Map[string](store2, "first")
	if err != nil {
		t.Fatalf("Failed to reload persist map 'first': %v", err)
	}
	reloadedMap2, err := Map[int](store2, "second")
	if err != nil {
		t.Fatalf("Failed to reload persist map 'second': %v", err)
	}

	// Validate map1.
	val, ok := reloadedMap1.Get("key1")
	if !ok {
		t.Fatalf("Failed to get 'key1' from map1")
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
