package persist

import (
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
)

// createTempStore creates a temporary WAL file and returns a new Store instance.
// It also registers cleanup functions to remove the temporary file and close the store.
func createTempStore(t *testing.T) (*Store, string) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "persist_test_*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	path := tmpFile.Name()
	tmpFile.Close()

	// Remove temporary file on test cleanup
	t.Cleanup(func() {
		os.Remove(path)
	})

	store, err := Open(path)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Ensure the store is closed on test cleanup
	t.Cleanup(func() {
		store.Close()
	})
	return store, path
}

// TestStore_SetGetAndDelete tests basic Set, Get and Delete operations
func TestStore_SetGetAndDelete(t *testing.T) {
	store, _ := createTempStore(t)

	type testStruct struct {
		A int
		B string
	}
	ts := testStruct{A: 42, B: "Hello"}

	// Set a key-value pair with struct value
	// English comment: set the key "structKey" with a struct value
	if err := store.Set("structKey", ts); err != nil {
		t.Fatalf("failed to set key 'structKey': %v", err)
	}

	// Get the struct value
	// English comment: retrieve the struct value and compare with the original
	var resultStruct testStruct
	if err := store.Get("structKey", &resultStruct); err != nil {
		t.Fatalf("failed to get key 'structKey': %v", err)
	}
	if resultStruct.A != ts.A || resultStruct.B != ts.B {
		t.Fatalf("expected struct %v, got %v", ts, resultStruct)
	}

	// Delete the struct key
	if err := store.Delete("structKey"); err != nil {
		t.Fatalf("failed to delete key 'structKey': %v", err)
	}

	// Getting the deleted struct key should return ErrKeyNotFound
	if err := store.Get("structKey", &resultStruct); err == nil {
		t.Fatal("expected error when getting deleted struct key, got nil")
	} else if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound error for struct key, got: %v", err)
	}
}

// TestStore_Shrink tests the compaction (Shrink) functionality
func TestStore_Shrink(t *testing.T) {
	store, _ := createTempStore(t)

	// Set several keys
	keys := []string{"a", "b", "c"}
	for i, k := range keys {
		if err := store.Set(k, i); err != nil {
			t.Fatalf("failed to set key '%s': %v", k, err)
		}
	}

	// Overwrite key "a" with a new value
	if err := store.Set("a", 100); err != nil {
		t.Fatalf("failed to update key 'a': %v", err)
	}

	// Delete key "b".
	if err := store.Delete("b"); err != nil {
		t.Fatalf("failed to delete key 'b': %v", err)
	}

	// Perform the shrink (compaction) operation
	if err := store.Shrink(); err != nil {
		t.Fatalf("failed to shrink store: %v", err)
	}

	// Verify that key "a" holds the updated value
	var valA int
	if err := store.Get("a", &valA); err != nil {
		t.Fatalf("failed to get key 'a': %v", err)
	}
	if valA != 100 {
		t.Fatalf("expected key 'a' to have value 100, got %d", valA)
	}

	// Verify that key "c" retains own value
	var valC int
	if err := store.Get("c", &valC); err != nil {
		t.Fatalf("failed to get key 'c': %v", err)
	}
	if valC != 2 {
		t.Fatalf("expected key 'c' to have value 2, got %d", valC)
	}

	// Verify that key "b" (deleted) not found
	var dummy int
	if err := store.Get("b", &dummy); err == nil {
		t.Fatal("expected error when getting deleted key 'b', got nil")
	} else if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound for key 'b', got: %v", err)
	}
}

// TestStore_Concurrent tests concurrent set operations to verify thread-safety
func TestStore_Concurrent(t *testing.T) {
	store, _ := createTempStore(t)

	var wg sync.WaitGroup
	numRoutines := 20
	numKeysPerRoutine := 10

	// Launch several goroutine, each writing several keys
	for i := 0; i < numRoutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < numKeysPerRoutine; j++ {
				key := "key_" + strconv.Itoa(i) + "_" + strconv.Itoa(j)
				if err := store.Set(key, j); err != nil {
					t.Errorf("failed to set key '%s': %v", key, err)
				}
			}
		}(i)
	}
	wg.Wait()

	// Verify that all keys have been set correctly
	for i := 0; i < numRoutines; i++ {
		for j := 0; j < numKeysPerRoutine; j++ {
			key := "key_" + strconv.Itoa(i) + "_" + strconv.Itoa(j)
			var value int
			if err := store.Get(key, &value); err != nil {
				t.Errorf("failed to get key '%s': %v", key, err)
			}
			if value != j {
				t.Errorf("expected key '%s' to have value %d, got %d", key, j, value)
			}
		}
	}
}

// TestStore_IncompleteRecord tests the behavior when an incomplete (truncated) record is present in the WAL file
func TestStore_IncompleteRecord(t *testing.T) {
	// Create a temporary file
	tmpFile, err := os.CreateTemp("", "persist_incomplete_test_*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	path := tmpFile.Name()
	t.Cleanup(func() {
		os.Remove(path)
	})

	// Write the WAL header
	if _, err := tmpFile.WriteString(WalHeader + "\n"); err != nil {
		t.Fatalf("failed to write WAL header: %v", err)
	}

	// Write a complete record for key "complete"
	// Record format:
	//   "S complete"
	//   "123" (a valid JSON number)
	if _, err := tmpFile.WriteString("S complete\n"); err != nil {
		t.Fatalf("failed to write complete record header: %v", err)
	}
	if _, err := tmpFile.WriteString("123\n"); err != nil {
		t.Fatalf("failed to write complete record value: %v", err)
	}

	// Write an incomplete record for key "incomplete" (missing value line)
	if _, err := tmpFile.WriteString("S incomplete\n"); err != nil {
		t.Fatalf("failed to write incomplete record header: %v", err)
	}

	// Flush writes to disk and close temporary file
	if err := tmpFile.Sync(); err != nil {
		t.Fatalf("failed to sync temp file: %v", err)
	}
	tmpFile.Close()

	// Reopen the store using the same WAL file with an incomplete record at the end.
	store, err := Open(path)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Test that the complete record for key "complete" is read correctly.
	var validVal int
	if err := store.Get("complete", &validVal); err != nil {
		t.Fatalf("failed to get key 'complete': %v", err)
	}
	if validVal != 123 {
		t.Fatalf("expected value 123 for key 'complete', got %d", validVal)
	}

	// Test that attempting to get the incomplete record results in ErrKeyNotFound.
	var incompleteVal int
	err = store.Get("incomplete", &incompleteVal)
	if err == nil {
		t.Fatal("expected error when getting key with incomplete record, got nil")
	} else if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound for key 'incomplete', got: %v", err)
	}
}

// TestStore_RecordValueWithoutNewline tests that if the last record's value does not end with a newline,
// then the record is treated as incomplete and skipped.
func TestStore_RecordValueWithoutNewline(t *testing.T) {
	// Create a temporary file for testing.
	tmpFile, err := os.CreateTemp("", "persist_no_newline_test_*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	path := tmpFile.Name()
	t.Cleanup(func() {
		os.Remove(path)
	})

	// Write WAL header (must be correctly terminated).
	if _, err := tmpFile.WriteString(WalHeader + "\n"); err != nil {
		t.Fatalf("failed to write WAL header: %v", err)
	}

	// Write a complete record for key "valid".
	// Record format:
	//   "S valid\n"
	//   "789\n" (a proper JSON number with trailing newline)
	if _, err := tmpFile.WriteString("S valid\n"); err != nil {
		t.Fatalf("failed to write header for key 'valid': %v", err)
	}
	if _, err := tmpFile.WriteString("789\n"); err != nil {
		t.Fatalf("failed to write value for key 'valid': %v", err)
	}

	// Write an incomplete record for key "incomplete" where the value line is not terminated by a newline.
	// Record format:
	//   "S incomplete\n"
	//   "456" (no trailing newline)
	if _, err := tmpFile.WriteString("S incomplete\n"); err != nil {
		t.Fatalf("failed to write header for key 'incomplete': %v", err)
	}
	if _, err := tmpFile.WriteString("456"); err != nil { // no newline at the end
		t.Fatalf("failed to write value for key 'incomplete': %v", err)
	}

	// Flush writes and close the temporary file.
	if err := tmpFile.Sync(); err != nil {
		t.Fatalf("failed to sync temp file: %v", err)
	}
	tmpFile.Close()

	// Open the store using the temporary WAL file with the incomplete record.
	store, err := Open(path)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	// Validate that the complete record for key "valid" can be read successfully.
	var validValue int
	if err := store.Get("valid", &validValue); err != nil {
		t.Fatalf("failed to get key 'valid': %v", err)
	}
	if validValue != 789 {
		t.Fatalf("expected key 'valid' to have value 789, got %d", validValue)
	}

	// Validate that the incomplete record for key "incomplete" is skipped.
	var incompleteValue int
	err = store.Get("incomplete", &incompleteValue)
	if err == nil {
		t.Fatal("expected error when retrieving key 'incomplete' with incomplete record (missing newline), got nil")
	} else if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound for key 'incomplete', got: %v", err)
	}
}

// TestStore_InvalidRecordMidFile tests the behavior when incorrect (corrupt) lines are present in the middle of the WAL file.
func TestStore_InvalidRecordMidFile(t *testing.T) {
	// Create a temporary file for the WAL
	tmpFile, err := os.CreateTemp("", "persist_invalid_mid_test_*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	path := tmpFile.Name()
	t.Cleanup(func() {
		os.Remove(path)
	})

	// Write WAL header (must be correctly terminated)
	if _, err := tmpFile.WriteString(WalHeader + "\n"); err != nil {
		t.Fatalf("failed to write WAL header: %v", err)
	}

	// Write a valid record for key "first"
	// Record format:
	//   "S first\n"
	//   "100\n" (valid JSON number)
	if _, err := tmpFile.WriteString("S first\n"); err != nil {
		t.Fatalf("failed to write record header for key 'first': %v", err)
	}
	if _, err := tmpFile.WriteString("100\n"); err != nil {
		t.Fatalf("failed to write record value for key 'first': %v", err)
	}

	// Write an invalid (corrupt) record in the middle
	// This header line does not follow the "op key" format (missing space)
	if _, err := tmpFile.WriteString("INVALID_RECORD\n"); err != nil {
		t.Fatalf("failed to write invalid record header: %v", err)
	}
	// Write an arbitrary value line to complete the malformed record
	if _, err := tmpFile.WriteString("garbage\n"); err != nil {
		t.Fatalf("failed to write invalid record value: %v", err)
	}

	// Write another valid record for key "second"
	// Even though this record is valid, it will be ignored because the reading process stops at the corrupt record.
	if _, err := tmpFile.WriteString("S second\n"); err != nil {
		t.Fatalf("failed to write record header for key 'second': %v", err)
	}
	if _, err := tmpFile.WriteString("200\n"); err != nil {
		t.Fatalf("failed to write record value for key 'second': %v", err)
	}

	if err := tmpFile.Sync(); err != nil {
		t.Fatalf("failed to sync temp file: %v", err)
	}
	tmpFile.Close()

	// Open the store using the existing file
	store, err := Open(path)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	// Attempt to retrieve key "first" which was written before the invalid record.
	// Expect to get the value 100.
	var firstVal int
	if err := store.Get("first", &firstVal); err != nil {
		t.Errorf("failed to get key 'first': %v", err)
	}
	if firstVal != 100 {
		t.Errorf("expected key 'first' to have value 100, got %d", firstVal)
	}

	// Attempt to retrieve key "second" which was written after the invalid record.
	// Since the invalid record stops further processing, key "second" should not be found.
	var secondVal int
	if err := store.Get("second", &secondVal); err == nil {
		t.Errorf("expected error for key 'second' due to invalid record mid file, got value %d", secondVal)
	} else if !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound for key 'second', got: %v", err)
	}
}
