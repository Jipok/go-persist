package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Jipok/go-persist"
	"github.com/cockroachdb/pebble"
	"github.com/dgraph-io/badger/v4"
	"github.com/goccy/go-json"
	"github.com/tidwall/buntdb"
	"github.com/voidDB/voidDB"
	bolt "go.etcd.io/bbolt"
)

// Number of entries per map
const numEntries = 81920 / 2

// Complex metadata for a record.
type Meta struct {
	CreatedAt int64    // record creation timestamp
	UpdatedAt int64    // record last update timestamp
	Tags      []string // list of tags
}

// ComplexRecord is a more complex structure with multiple fields.
type ComplexRecord struct {
	ID          string // record identifier
	Name        string // record name
	Description string // record description
	Data        string // fixed-size data payload
	Meta        Meta   // nested metadata
}

// flushPageCache flushes the filesystem page cache.
// NOTE: This requires root privileges.
func flushPageCache() {
	// Execute sync command to flush file system buffers
	if err := exec.Command("sync").Run(); err != nil {
		log.Fatal("flushPageCache: sync: ", err)
	}
	// Write "3" to /proc/sys/vm/drop_caches to drop page cache
	// Using tee to handle shell redirection under bash.
	err := exec.Command("bash", "-c", "echo 3 > /proc/sys/vm/drop_caches").Run()
	if err != nil {
		log.Fatal("flushPageCache: drop_caches: ", err)
	}
	time.Sleep(time.Second)
}

// readFileToMemory reads the entire file into memory as a byte slice.
// It opens the file, obtains its size, preallocates a buffer and reads the data.
// This approach minimizes memory reallocations for large files.
func readFileToMemory(filename string) (int, error) {
	// Open the file for reading
	file, err := os.Open(filename)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	// Get the file information
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}

	// Allocate a buffer with the exact file size
	size := info.Size()
	buffer := make([]byte, size)

	// Read the entire file into the buffer
	_, err = io.ReadFull(file, buffer)
	if err != nil {
		return 0, err
	}

	return len(buffer), nil
}

// dirSize calculates the total size of all files in the given directory.
func dirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

func memstr(alloc uint64) string {
	switch {
	case alloc <= 1024:
		return fmt.Sprintf("%d bytes", alloc)
	case alloc <= 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(alloc)/1024)
	case alloc <= 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(alloc)/1024/1024)
	default:
		return fmt.Sprintf("%.1f GB", float64(alloc)/1024/1024/1024)
	}
}

func measure(name string, f func()) {
	flushPageCache()
	start := time.Now()
	var ms1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms1)

	f()
	fmt.Printf("%s read time: %.2fs\n", name, time.Since(start).Seconds())

	var ms2 runtime.MemStats
	var alloc uint64
	runtime.ReadMemStats(&ms2)
	alloc = ms2.HeapAlloc - ms1.HeapAlloc
	fmt.Printf("%s mem usage: %s\n", name, memstr(alloc))
}

var payload string

// createRecord generates a new ComplexRecord for a given map name and index
func createRecord(mapName string, i int) ComplexRecord {
	now := time.Now().Unix()
	return ComplexRecord{
		ID:          fmt.Sprintf("%s-%d", mapName, i),
		Name:        fmt.Sprintf("Record %d", i),
		Description: "This is a sample description for a complex record.",
		Data:        payload,
		Meta: Meta{
			CreatedAt: now,
			UpdatedAt: now,
			Tags:      []string{"tag1", "tag2", "tag3"},
		},
	}
}

func runPersist() {
	// Names for 5 maps stored in the same file
	mapNames := []string{"complex_map1", "complex_map2", "complex_map3", "complex_map4", "complex_map5"}

	// --- PART 1: Generate data ---
	if _, err := os.Stat("persist.db"); errors.Is(err, os.ErrNotExist) {
		flushPageCache()
		start := time.Now()

		store := persist.New()
		// Create an array to hold the maps
		maps := make([]*persist.PersistMap[ComplexRecord], len(mapNames))
		// Create each typed map
		for i, name := range mapNames {
			m, err := persist.Map[ComplexRecord](store, name)
			if err != nil {
				log.Fatalf("Failed to create map %s: %v", name, err)
			}
			maps[i] = m
		}

		err := store.Open("persist.db")
		if err != nil {
			log.Fatal(err)
		}

		// Populate each map with generated records.
		for mapIndex, m := range maps {
			for i := 0; i < numEntries; i++ {
				key := fmt.Sprintf("key-%d", i) // key format: "key-i"
				record := createRecord(mapNames[mapIndex], i)
				m.Set(key, record)
			}
		}

		// Close the store to flush all data to disk.
		store.Close()
		loadDuration := time.Since(start)
		fmt.Printf("Persist write time: %.2fs\n", loadDuration.Seconds())
		return
	}

	// --- PART 2: Measure map loading time ---
	flushPageCache()
	start := time.Now()
	size, _ := readFileToMemory("persist.db")
	println("Persist file size: ", size/1024/1024, " MB")
	fmt.Printf("Persist raw load time: %.2fs\n", time.Since(start).Seconds())

	measure("Persist one", func() {
		store := persist.New()
		defer store.Close()

		maps := make([]*persist.PersistMap[ComplexRecord], len(mapNames))
		for i, name := range mapNames {
			m, err := persist.Map[ComplexRecord](store, name)
			if err != nil {
				log.Fatalf("Failed to create map %s: %v", name, err)
			}
			maps[i] = m
		}

		err := store.Open("persist.db")
		if err != nil {
			log.Fatal(err)
		}
		runtime.GC()

		r, ok := maps[2].Get("key-40000")
		if !ok {
			log.Fatal("key not found")
		}
		if r.ID != "complex_map3-40000" {
			log.Fatal("Wrong value: ", r.ID)
		}
	})

	measure("Persist 40k", func() {
		store := persist.New()
		defer store.Close()

		// Create and initialize maps as in the one-key test.
		maps := make([]*persist.PersistMap[ComplexRecord], len(mapNames))
		for i, name := range mapNames {
			m, err := persist.Map[ComplexRecord](store, name)
			if err != nil {
				log.Fatalf("Failed to create map %s: %v", name, err)
			}
			maps[i] = m
		}

		err := store.Open("persist.db")
		if err != nil {
			log.Fatal(err)
		}
		runtime.GC()

		for i := 0; i < 40000; i++ {
			key := fmt.Sprintf("key-%d", i) // key format: "key-i"
			r, ok := maps[2].Get(key)
			if !ok {
				log.Fatalf("Persist key %s not found", key)
			}
			expectedID := fmt.Sprintf("complex_map3-%d", i)
			if r.ID != expectedID {
				log.Fatalf("Persist wrong value for key %s: got %s, expected %s", key, r.ID, expectedID)
			}
		}
	})

}

func runBoltDB() {
	// Names for 5 buckets stored in the same file
	mapNames := []string{"bolt_map1", "bolt_map2", "bolt_map3", "bolt_map4", "bolt_map5"}

	// Open the BoltDB file.

	if _, err := os.Stat("bolt.db"); errors.Is(err, os.ErrNotExist) {
		flushPageCache()
		start := time.Now()

		db, err := bolt.Open("bolt.db", 0666, &bolt.Options{NoSync: true})
		if err != nil {
			log.Fatal(err)
		}

		// Create buckets and insert data.
		err = db.Update(func(tx *bolt.Tx) error {
			// Loop over each bucket name.
			for _, bucketName := range mapNames {
				bucket, err := tx.CreateBucketIfNotExists([]byte(bucketName))
				if err != nil {
					return err
				}
				// Insert generated records into the bucket.
				for i := 0; i < numEntries; i++ {
					key := fmt.Sprintf("key-%d", i)
					record := createRecord(bucketName, i)
					// Marshal the record into JSON.
					encoded, err := json.Marshal(record)
					if err != nil {
						return err
					}
					// Put the key/value pair into the bucket.
					err = bucket.Put([]byte(key), encoded)
					if err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			log.Fatal(err)
		}

		err = db.Close()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("BoltDB  write time: %.2fs\n", time.Since(start).Seconds())
		return
	}

	// Measure raw load time for BoltDB.
	flushPageCache()
	start := time.Now()
	size, _ := readFileToMemory("bolt.db")
	println("BoltDB file size: ", size/1024/1024, " MB")
	fmt.Printf("BoltDB raw load time: %.2fs\n", time.Since(start).Seconds())

	measure("BoltDB one", func() {
		db, err := bolt.Open("bolt.db", 0666, nil)
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()
		runtime.GC()

		var record ComplexRecord
		err = db.View(func(tx *bolt.Tx) error {
			bucket := tx.Bucket([]byte("bolt_map3"))
			if bucket == nil {
				return fmt.Errorf("bucket bolt_map3 not found")
			}
			value := bucket.Get([]byte("key-40000"))
			if value == nil {
				return fmt.Errorf("key not found")
			}
			// Unmarshal JSON value into the record.
			return json.Unmarshal(value, &record)
		})
		if err != nil {
			log.Fatal(err)
		}

		if record.ID != "bolt_map3-40000" {
			log.Fatal("Wrong value: ", record.ID)
		}
	})

	measure("BoltDB 40k", func() {
		db, err := bolt.Open("bolt.db", 0666, nil)
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()
		runtime.GC()

		err = db.View(func(tx *bolt.Tx) error {
			bucket := tx.Bucket([]byte("bolt_map3"))
			if bucket == nil {
				return fmt.Errorf("bucket bolt_map3 not found")
			}

			// Loop to read 10,000 keys.
			for i := 0; i < 40000; i++ {
				key := fmt.Sprintf("key-%d", i)
				value := bucket.Get([]byte(key))
				if value == nil {
					return fmt.Errorf("BoltDB key %s not found", key)
				}
				var record ComplexRecord
				// Unmarshal JSON into record.
				if err := json.Unmarshal(value, &record); err != nil {
					return err
				}
				expectedID := fmt.Sprintf("bolt_map3-%d", i)
				if record.ID != expectedID {
					return fmt.Errorf("BoltDB wrong value for key %s: got %s, expected %s", key, record.ID, expectedID)
				}
			}
			return nil
		})
		if err != nil {
			log.Fatal(err)
		}
	})
}

func runBuntDB() {
	// Names for 5 logical maps (using key prefixes)
	mapNames := []string{"bunt_map1", "bunt_map2", "bunt_map3", "bunt_map4", "bunt_map5"}

	if _, err := os.Stat("bunt.db"); errors.Is(err, os.ErrNotExist) {
		flushPageCache()
		start := time.Now()

		db, err := buntdb.Open("bunt.db")
		if err != nil {
			log.Fatal(err)
		}

		// Insert records for each map.
		err = db.Update(func(tx *buntdb.Tx) error {
			for _, mapName := range mapNames {
				for i := 0; i < numEntries; i++ {
					// Construct key using map name as prefix.
					key := fmt.Sprintf("%s:key-%d", mapName, i)
					record := createRecord(mapName, i)
					encoded, err := json.Marshal(record)
					if err != nil {
						return err
					}
					// Set the key with the JSON encoded record.
					_, _, err = tx.Set(key, string(encoded), nil)
					if err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			log.Fatal(err)
		}

		err = db.Close()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("BuntDB  write time: %.2fs\n", time.Since(start).Seconds())
		return
	}

	// Measure raw load time for BuntDB.
	flushPageCache()
	start := time.Now()
	size, _ := readFileToMemory("bunt.db")
	println("BuntDB file size: ", size/1024/1024, " MB")
	fmt.Printf("BuntDB raw load time: %.2fs\n", time.Since(start).Seconds())

	// Measure read time.
	measure("BuntDB one", func() {
		db, err := buntdb.Open("bunt.db")
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()
		runtime.GC()

		var record ComplexRecord
		err = db.View(func(tx *buntdb.Tx) error {
			val, err := tx.Get("bunt_map3:key-40000")
			if err != nil {
				return err
			}
			// Unmarshal the JSON record.
			return json.Unmarshal([]byte(val), &record)
		})
		if err != nil {
			log.Fatal(err)
		}
		if record.ID != "bunt_map3-40000" {
			log.Fatal("Wrong value: ", record.ID)
		}
	})

	measure("BuntDB 40k", func() {
		db, err := buntdb.Open("bunt.db")
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()
		runtime.GC()

		err = db.View(func(tx *buntdb.Tx) error {
			for i := 0; i < 40000; i++ {
				key := fmt.Sprintf("bunt_map3:key-%d", i)
				val, err := tx.Get(key)
				if err != nil {
					return err
				}
				var record ComplexRecord
				// Unmarshal the JSON record.
				if err := json.Unmarshal([]byte(val), &record); err != nil {
					return err
				}
				expectedID := fmt.Sprintf("bunt_map3-%d", i)
				if record.ID != expectedID {
					return fmt.Errorf("BuntDB wrong value for key %s: got %s, expected %s", key, record.ID, expectedID)
				}
			}
			return nil
		})
		if err != nil {
			log.Fatal(err)
		}
	})

}

func runPebble() {
	// Names for 5 logical maps using key prefixes
	mapNames := []string{"pebble_map1", "pebble_map2", "pebble_map3", "pebble_map4", "pebble_map5"}

	// If the Pebble database does not exist, generate the data.
	if _, err := os.Stat("pebble.db"); errors.Is(err, os.ErrNotExist) {
		flushPageCache()
		start := time.Now()

		db, err := pebble.Open("pebble.db", &pebble.Options{Logger: discardLogger{}})
		if err != nil {
			log.Fatal(err)
		}

		// Insert records for each logical map.
		for _, mapName := range mapNames {
			for i := 0; i < numEntries; i++ {
				// Construct key using map name as prefix.
				key := fmt.Sprintf("%s:key-%d", mapName, i)
				record := createRecord(mapName, i)
				// Marshal the record into JSON.
				encoded, err := json.Marshal(record)
				if err != nil {
					log.Fatal(err)
				}
				// Set the key/value pair in Pebble.
				// Using pebble.Sync to ensure data is flushed.
				if err := db.Set([]byte(key), encoded, pebble.NoSync); err != nil {
					log.Fatal(err)
				}
			}
		}

		if err := db.Close(); err != nil {
			log.Fatal(err)
		}

		fmt.Printf("Pebble  write time: %.2fs\n", time.Since(start).Seconds())
		return
	}

	// If the database exists, measure raw load time and read performance.
	flushPageCache()
	size, err := dirSize("pebble.db")
	if err != nil {
		log.Fatal(err)
	}
	println("Pebble dir size: ", size/1024/1024, " MB")

	measure("Pebble one", func() {
		db, err := pebble.Open("pebble.db", &pebble.Options{Logger: discardLogger{}})
		if err != nil {
			log.Fatal(err)
		}
		defer func() {
			if err := db.Close(); err != nil {
				log.Fatal(err)
			}
		}()
		runtime.GC()

		// Retrieve a single key from 'pebble_map3'
		key := "pebble_map3:key-40000"
		value, closer, err := db.Get([]byte(key))
		if err != nil {
			log.Fatal(err)
		}
		// Close the returned closer to free memory.
		if closer != nil {
			closer.Close()
		}
		var record ComplexRecord
		if err := json.Unmarshal(value, &record); err != nil {
			log.Fatal(err)
		}
		if record.ID != "pebble_map3-40000" {
			log.Fatalf("Wrong value: %s", record.ID)
		}
	})

	measure("Pebble 40k", func() {
		db, err := pebble.Open("pebble.db", &pebble.Options{Logger: discardLogger{}})
		if err != nil {
			log.Fatal(err)
		}
		defer func() {
			if err := db.Close(); err != nil {
				log.Fatal(err)
			}
		}()
		runtime.GC()

		// Loop to read 40,000 keys.
		for i := 0; i < 40000; i++ {
			key := fmt.Sprintf("pebble_map3:key-%d", i)
			value, closer, err := db.Get([]byte(key))
			if err != nil {
				log.Fatalf("Pebble key %s not found: %v", key, err)
			}
			if closer != nil {
				closer.Close()
			}
			var record ComplexRecord
			if err := json.Unmarshal(value, &record); err != nil {
				log.Fatal(err)
			}
			expectedID := fmt.Sprintf("pebble_map3-%d", i)
			if record.ID != expectedID {
				log.Fatalf("Pebble wrong value for key %s: got %s, expected %s", key, record.ID, expectedID)
			}
		}
	})
}

func runBadger() {
	// Names for 5 logical maps (using key prefixes)
	mapNames := []string{"badger_map1", "badger_map2", "badger_map3", "badger_map4", "badger_map5"}

	// Check if the "badger" directory exists.
	if _, err := os.Stat("badger"); errors.Is(err, os.ErrNotExist) {
		flushPageCache()
		start := time.Now()

		// Open Badger DB with directory "badger"
		opts := badger.DefaultOptions("badger")
		opts.SyncWrites = false
		opts.Logger = nil
		db, err := badger.Open(opts)
		if err != nil {
			log.Fatal(err)
		}

		// Create a fixed payload of 1024 bytes using the character 'x'
		dataPayloadBytes := make([]byte, 1024)
		for i := range dataPayloadBytes {
			dataPayloadBytes[i] = 'x'
		}
		dataPayload := string(dataPayloadBytes)

		// Use WriteBatch for efficient batch writes
		wb := db.NewWriteBatch()
		defer wb.Cancel()
		batchCount := 0

		// Insert records for each map.
		for _, mapName := range mapNames {
			for i := 0; i < numEntries; i++ {
				key := fmt.Sprintf("%s:key-%d", mapName, i)
				record := ComplexRecord{
					ID:          fmt.Sprintf("%s-%d", mapName, i),
					Name:        fmt.Sprintf("Record %d", i),
					Description: "This is a sample description for a complex record.",
					Data:        dataPayload,
					Meta: Meta{
						CreatedAt: time.Now().Unix(),
						UpdatedAt: time.Now().Unix(),
						Tags:      []string{"tag1", "tag2", "tag3"},
					},
				}
				// Marshal the record into JSON.
				encoded, err := json.Marshal(record)
				if err != nil {
					log.Fatal(err)
				}
				// Set the key/value pair.
				err = wb.Set([]byte(key), encoded)
				if err != nil {
					log.Fatal(err)
				}
				batchCount++
				// Flush periodically every 1000 entries.
				// if batchCount%1000 == 0 {
				// 	if err := wb.Flush(); err != nil {
				// 		log.Fatal(err)
				// 	}
				// }
			}
		}
		// Flush any remaining writes.
		if err := wb.Flush(); err != nil {
			log.Fatal(err)
		}

		if err := db.Close(); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Badger  write time: %.2fs\n", time.Since(start).Seconds())
		return
	}

	// --- PART 2: Measure DB loading time ---

	flushPageCache()
	// Calculate and print the total size of the "badger" directory.
	dirSz, err := dirSize("badger")
	if err != nil {
		log.Fatal(err)
	}
	println("Badger DB dir size: ", dirSz/1024/1024, " MB")

	measure("Badger one", func() {
		opts := badger.DefaultOptions("badger")
		opts.SyncWrites = false
		opts.Logger = nil
		db, err := badger.Open(opts)
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()
		runtime.GC()

		var record ComplexRecord
		err = db.View(func(txn *badger.Txn) error {
			// Get the record from the "badger_map3" set.
			item, err := txn.Get([]byte("badger_map3:key-40000"))
			if err != nil {
				return err
			}
			// Unmarshal the JSON value.
			return item.Value(func(val []byte) error {
				return json.Unmarshal(val, &record)
			})
		})
		if err != nil {
			log.Fatal(err)
		}

		if record.ID != "badger_map3-40000" {
			log.Fatal("Wrong value: ", record.ID)
		}
	})

	measure("Badger 40k", func() {
		opts := badger.DefaultOptions("badger")
		opts.SyncWrites = false
		opts.Logger = nil
		db, err := badger.Open(opts)
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()
		runtime.GC()

		err = db.View(func(txn *badger.Txn) error {
			for i := 0; i < 40000; i++ {
				key := fmt.Sprintf("badger_map3:key-%d", i)
				item, err := txn.Get([]byte(key))
				if err != nil {
					return err
				}
				var record ComplexRecord
				// Unmarshal the JSON value.
				if err := item.Value(func(val []byte) error {
					return json.Unmarshal(val, &record)
				}); err != nil {
					return err
				}
				expectedID := fmt.Sprintf("badger_map3-%d", i)
				if record.ID != expectedID {
					return fmt.Errorf("badger wrong value for key %s: got %s, expected %s", key, record.ID, expectedID)
				}
			}
			return nil
		})
		if err != nil {
			log.Fatal(err)
		}
	})
}

func runVoidDB() {
	// Names for 5 logical maps using keyspaces
	mapNames := []string{"void_map1", "void_map2", "void_map3", "void_map4", "void_map5"}
	const capacity = 1024 * 1024 * 1024 * 20 // 20 GB
	const path = "void"

	// Check if the voidDB directory exists; if not, populate with data
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		flushPageCache()
		start := time.Now()

		// Initialize a new voidDB instance
		v, err := voidDB.NewVoid(path, capacity)
		if err != nil {
			log.Fatal(err)
		}

		// Insert records for each logical map in separate update transactions
		for _, mapName := range mapNames {
			mustSync := false
			err = v.Update(mustSync, func(txn *voidDB.Txn) error {
				// Open a cursor for the given keyspace (map)
				cur, err := txn.OpenCursor([]byte(mapName))
				if err != nil {
					return err
				}
				// Insert generated records into the keyspace
				for i := 0; i < numEntries; i++ {
					key := []byte(fmt.Sprintf("key-%d", i))
					record := createRecord(mapName, i)
					encoded, err := json.Marshal(record)
					if err != nil {
						return err
					}
					// Put the key/value pair into the keyspace
					if err := cur.Put(key, encoded); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				log.Fatal(err)
			}
		}

		// Close the voidDB instance to ensure all data is flushed to disk
		if err := v.Close(); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("VoidDB  write time: %.2fs\n", time.Since(start).Seconds())
		return
	}

	// --- PART 2: Measure DB loading and read performance ---

	flushPageCache()
	// Calculate and print the total size of the "void" directory
	size, err := dirSize(path)
	if err != nil {
		log.Fatal(err)
	}
	println("VoidDB dir size: ", size/1024/1024, " MB")

	measure("VoidDB one", func() {
		v, err := voidDB.OpenVoid(path, capacity)
		if err != nil {
			log.Fatal(err)
		}
		defer v.Close()
		runtime.GC()

		// Use a read-only transaction for retrieving a single record
		err = v.View(func(txn *voidDB.Txn) error {
			// Open a cursor for the "void_map3" keyspace
			cur, err := txn.OpenCursor([]byte("void_map3"))
			if err != nil {
				return err
			}
			// Retrieve the record with key "key-40000"
			val, err := cur.Get([]byte("key-40000"))
			if err != nil {
				return err
			}
			var record ComplexRecord
			if err := json.Unmarshal(val, &record); err != nil {
				return err
			}
			if record.ID != "void_map3-40000" {
				return fmt.Errorf("Wrong value: %s", record.ID)
			}
			return nil
		})
		if err != nil {
			log.Fatal(err)
		}
	})

	measure("VoidDB 40k", func() {
		v, err := voidDB.OpenVoid(path, capacity)
		if err != nil {
			log.Fatal(err)
		}
		defer v.Close()
		runtime.GC()

		// Use a read-only transaction for retrieving 40k records
		err = v.View(func(txn *voidDB.Txn) error {
			// Open a cursor for the "void_map3" keyspace
			cur, err := txn.OpenCursor([]byte("void_map3"))
			if err != nil {
				return err
			}
			// Loop to read 40,000 keys
			for i := 0; i < 40000; i++ {
				key := []byte(fmt.Sprintf("key-%d", i))
				val, err := cur.Get(key)
				if err != nil {
					return fmt.Errorf("VoidDB key %s not found: %v", key, err)
				}
				var record ComplexRecord
				if err := json.Unmarshal(val, &record); err != nil {
					return err
				}
				expectedID := fmt.Sprintf("void_map3-%d", i)
				if record.ID != expectedID {
					return fmt.Errorf("VoidDB wrong value for key %s: got %s, expected %s", key, record.ID, expectedID)
				}
			}
			return nil
		})
		if err != nil {
			log.Fatal(err)
		}
	})
}

func main() {
	// Prefill a fixed payload of 1024 bytes using the character 'x'
	dataPayloadBytes := make([]byte, 1024)
	for i := range dataPayloadBytes {
		dataPayloadBytes[i] = 'x'
	}
	payload = string(dataPayloadBytes)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "persist":
			runPersist()
		case "bolt":
			runBoltDB()
		case "buntdb":
			runBuntDB()
		case "pebble":
			runPebble()
		case "badger":
			runBadger()
		case "void":
			runVoidDB()
		//
		case "seq":
			fmt.Println("\nSequential open-read test (1000 iterations) without flushing disk cache:")
			sequentialOpenBolt()
			sequentialOpenPebble()
			sequentialOpenBadger()
			sequentialOpenVoid()
		default:
			log.Fatal("Unknown db")
		}
	} else {
		runPersist()
		println()
		runBoltDB()
		println()
		runBuntDB()
		println()
		runPebble()
		println()
		runBadger()
		println()
		runVoidDB() // voidDB: write would exceed scope of memory map
	}
}
