package main

import (
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/dgraph-io/badger/v4"
	"github.com/voidDB/voidDB"
	bolt "go.etcd.io/bbolt"
)

const numFiles = 1000
const fileToCheck = 500
const fileKey = "file-500"

var files_payloads [][]byte

// createPayload returns a slice of the given size filled with random bytes.
func createPayload(size int, seed int64) []byte {
	b := make([]byte, size)
	r := rand.New(rand.NewSource(seed))
	if _, err := r.Read(b); err != nil {
		log.Fatalf("Error generating random payload: %v", err)
	}
	return b
}

// getFileSize returns a reproducible file size (in bytes) for a given file index.
// The size will vary between 2MB and 5MB even for small i values.
func getFileSize(i int) int {
	const MB = 1024 * 1024
	// Special-case: if i is 0, return exactly 2MB
	if i == 0 {
		return 2 * MB
	}
	h := fnv.New32a()
	// Write the string representation of i into the hash generator
	h.Write([]byte(strconv.Itoa(i)))
	// Get a hash sum which is then mapped into [0,1)
	hashValue := h.Sum32()
	r := float64(hashValue) / float64(math.MaxUint32)
	// Scale the random fraction to the full variation range (3MB)
	variation := int(r * float64(3*MB))
	return 2*MB + variation
}

// precomputePayloads generates random payloads for numFiles files
func precomputePayloads(numFiles int) {
	files_payloads = make([][]byte, numFiles)
	for i := 0; i < numFiles; i++ {
		size := getFileSize(i)
		files_payloads[i] = createPayload(size, int64(i))
	}
}

// writeFilesCommon executes a loop writing numFiles files using a provided writeFunc.
func writeFilesCommon(numFiles int, writeFunc func(i int, payload []byte) error) error {
	for i, payload := range files_payloads {
		if err := writeFunc(i, payload); err != nil {
			return err
		}
	}
	return nil
}

// runBoltFiles performs a benchmark for BoltDB by writing "files"
// with variable sizes (in the range 2–5 MB) and then measuring the access time for one file.
func runBoltFiles() {
	if _, err := os.Stat("bolt_files.db"); errors.Is(err, os.ErrNotExist) {
		flushPageCache()
		start := time.Now()

		db, err := bolt.Open("bolt_files.db", 0666, &bolt.Options{NoSync: true})
		if err != nil {
			log.Fatal(err)
		}

		err = db.Update(func(tx *bolt.Tx) error {
			bucket, err := tx.CreateBucketIfNotExists([]byte("files"))
			if err != nil {
				return err
			}
			// Insert files with variable sizes determined by getFileSize.
			return writeFilesCommon(numFiles, func(i int, payload []byte) error {
				key := fmt.Sprintf("file-%d", i)
				return bucket.Put([]byte(key), payload)
			})
		})
		if err != nil {
			log.Fatal(err)
		}
		if err := db.Sync(); err != nil {
			log.Fatal(err)
		}
		if err := db.Close(); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Bolt Files write time: %.2fs\n", time.Since(start).Seconds())
		// Output the database file size.
		printSize("bolt_files.db")
	}

	// Read phase: measure the access time for one file.
	flushPageCache()
	measure("Bolt Files one", func() {
		db, err := bolt.Open("bolt_files.db", 0666, nil)
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()

		err = db.View(func(tx *bolt.Tx) error {
			bucket := tx.Bucket([]byte("files"))
			if bucket == nil {
				return fmt.Errorf("bucket 'files' not found")
			}
			key := fileKey
			value := bucket.Get([]byte(key))
			expectedSize := getFileSize(fileToCheck)
			if value == nil || len(value) != expectedSize {
				return fmt.Errorf("file not found or incorrect size: got %d, expected %d", len(value), expectedSize)
			}
			return nil
		})
		if err != nil {
			log.Fatal(err)
		}
	})
}

// runPebbleFiles performs a benchmark for Pebble by writing "files"
// with variable sizes (in the range 2–5 MB) and then measuring the access time for one file.
func runPebbleFiles() {
	if _, err := os.Stat("pebble_files.db"); errors.Is(err, os.ErrNotExist) {
		flushPageCache()
		start := time.Now()

		db, err := pebble.Open("pebble_files.db", &pebble.Options{Logger: discardLogger{}})
		if err != nil {
			log.Fatal(err)
		}

		// Insert N files with variable sizes determined by getFileSize.
		if err := writeFilesCommon(numFiles, func(i int, payload []byte) error {
			key := fmt.Sprintf("file-%d", i)
			return db.Set([]byte(key), payload, pebble.NoSync)
		}); err != nil {
			log.Fatal(err)
		}
		if err := db.Close(); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Pebble Files write time: %.2fs\n", time.Since(start).Seconds())
		// Output the database file size.
		printSize("pebble_files.db")
	}

	// Read phase: measure the access time for one file.
	flushPageCache()
	measure("Pebble Files one", func() {
		db, err := pebble.Open("pebble_files.db", &pebble.Options{Logger: discardLogger{}})
		if err != nil {
			log.Fatal(err)
		}
		defer func() {
			if err := db.Close(); err != nil {
				log.Fatal(err)
			}
		}()

		key := fileKey
		value, closer, err := db.Get([]byte(key))
		if err != nil {
			log.Fatal(err)
		}
		if closer != nil {
			closer.Close()
		}
		expectedSize := getFileSize(fileToCheck)
		if len(value) != expectedSize {
			log.Fatalf("Pebble Files: file has incorrect size: got %d, expected %d", len(value), expectedSize)
		}
	})
}

// runBadgerFiles performs a benchmark for BadgerDB by writing "files"
// with variable sizes (in the range 2–5 MB) and then measuring the access time for one file.
func runBadgerFiles() {
	if _, err := os.Stat("badger_files"); errors.Is(err, os.ErrNotExist) {
		flushPageCache()
		start := time.Now()

		opts := badger.DefaultOptions("badger_files")
		opts.SyncWrites = false
		opts.Logger = nil
		db, err := badger.Open(opts)
		if err != nil {
			log.Fatal(err)
		}

		wb := db.NewWriteBatch()
		defer wb.Cancel()

		// Insert files with variable sizes determined by getFileSize.
		if err := writeFilesCommon(numFiles, func(i int, payload []byte) error {
			key := fmt.Sprintf("file-%d", i)
			return wb.Set([]byte(key), payload)
		}); err != nil {
			log.Fatal(err)
		}
		if err := wb.Flush(); err != nil {
			log.Fatal(err)
		}
		if err := db.Close(); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Badger Files write time: %.2fs\n", time.Since(start).Seconds())
		// Output the database directory size.
		printSize("badger_files")
	}

	// Read phase: measure the access time for one file.
	flushPageCache()
	measure("Badger Files one", func() {
		opts := badger.DefaultOptions("badger_files")
		opts.SyncWrites = false
		opts.Logger = nil
		db, err := badger.Open(opts)
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()

		err = db.View(func(txn *badger.Txn) error {
			key := []byte(fileKey)
			item, err := txn.Get(key)
			if err != nil {
				return err
			}
			var data []byte
			err = item.Value(func(val []byte) error {
				data = append([]byte{}, val...)
				return nil
			})
			if err != nil {
				return err
			}
			expectedSize := getFileSize(fileToCheck)
			if len(data) != expectedSize {
				return fmt.Errorf("Badger Files: file has incorrect size: got %d, expected %d", len(data), expectedSize)
			}
			return nil
		})
		if err != nil {
			log.Fatal(err)
		}
	})
}

func runVoidDBFiles() {
	const voidPath = "void_files.db"
	const capacity = 1024 * 1024 * 1024 * 20 // 1 TiB

	// If the voidDB does not already exist, perform the write phase.
	if _, err := os.Stat(voidPath); errors.Is(err, os.ErrNotExist) {
		flushPageCache()
		start := time.Now()

		void, err := voidDB.NewVoid(voidPath, capacity)
		if err != nil {
			log.Fatal(err)
		}
		defer void.Close()

		mustSync := true
		err = void.Update(mustSync, func(txn *voidDB.Txn) error {
			// Insert files with variable sizes determined by getFileSize.
			return writeFilesCommon(numFiles, func(i int, payload []byte) error {
				key := fmt.Sprintf("file-%d", i)
				// Put the key/value pair into the database.
				return txn.Put([]byte(key), payload)
			})
		})
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("voidDB Files write time: %.2fs\n", time.Since(start).Seconds())
		printSize(voidPath)
	}

	// Read phase: measure the access time for one file.
	flushPageCache()
	measure("voidDB Files one", func() {
		void, err := voidDB.OpenVoid(voidPath, capacity)
		if err != nil {
			log.Fatal(err)
		}
		defer void.Close()

		err = void.View(func(txn *voidDB.Txn) error {
			// Retrieve the file using the predefined key.
			value, err := txn.Get([]byte(fileKey))
			if err != nil {
				return err
			}
			expectedSize := getFileSize(fileToCheck)
			if len(value) != expectedSize {
				return fmt.Errorf("voidDB Files: file has incorrect size: got %d, expected %d", len(value), expectedSize)
			}
			return nil
		})
		if err != nil {
			log.Fatal(err)
		}
	})
}
