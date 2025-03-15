package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/Jipok/go-persist"
	"github.com/goccy/go-json"
	"github.com/tidwall/buntdb"
	bolt "go.etcd.io/bbolt"
)

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

// runPersist executes the original persist-based test.
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

		// Number of entries per map.
		// With a fixed payload of 1024 bytes per record,
		// 81920 entries will generate roughly 80 MB per map.
		numEntries := 81920

		// Create a fixed payload of 1024 bytes using the character 'x'
		dataPayloadBytes := make([]byte, 1024)
		for i := range dataPayloadBytes {
			dataPayloadBytes[i] = 'x'
		}
		dataPayload := string(dataPayloadBytes)

		// Populate each map with generated records.
		for mapIndex, m := range maps {
			for i := 0; i < numEntries; i++ {
				key := fmt.Sprintf("key-%d", i) // key format: "key-i"
				record := ComplexRecord{
					ID:          fmt.Sprintf("%s-%d", mapNames[mapIndex], i),
					Name:        fmt.Sprintf("Record %d", i),
					Description: "This is a sample description for a complex record.",
					Data:        dataPayload,
					Meta: Meta{
						CreatedAt: time.Now().Unix(),
						UpdatedAt: time.Now().Unix(),
						Tags:      []string{"tag1", "tag2", "tag3"},
					},
				}
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

		r, ok := maps[2].Get("key-60000")
		if !ok {
			log.Fatal("key not found")
		}
		if r.ID != "complex_map3-60000" {
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

// runBoltDB executes the test using BoltDB (bbolt).
func runBoltDB() {
	// Names for 5 buckets stored in the same file
	mapNames := []string{"bolt_map1", "bolt_map2", "bolt_map3", "bolt_map4", "bolt_map5"}

	// Open the BoltDB file.

	if _, err := os.Stat("bolt.db"); errors.Is(err, os.ErrNotExist) {
		flushPageCache()
		start := time.Now()

		db, err := bolt.Open("bolt.db", 0666, nil)
		if err != nil {
			log.Fatal(err)
		}

		numEntries := 81920
		// Create a fixed payload of 1024 bytes using the character 'x'
		dataPayloadBytes := make([]byte, 1024)
		for i := range dataPayloadBytes {
			dataPayloadBytes[i] = 'x'
		}
		dataPayload := string(dataPayloadBytes)

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
					record := ComplexRecord{
						ID:          fmt.Sprintf("%s-%d", bucketName, i),
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
				return fmt.Errorf("Bucket bolt_map3 not found")
			}
			value := bucket.Get([]byte("key-60000"))
			if value == nil {
				return fmt.Errorf("key not found")
			}
			// Unmarshal JSON value into the record.
			return json.Unmarshal(value, &record)
		})
		if err != nil {
			log.Fatal(err)
		}

		if record.ID != "bolt_map3-60000" {
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
				return fmt.Errorf("Bucket bolt_map3 not found")
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

// runBuntDB executes the test using BuntDB.
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

		numEntries := 81920
		// Create a fixed payload of 1024 bytes using the character 'x'
		dataPayloadBytes := make([]byte, 1024)
		for i := range dataPayloadBytes {
			dataPayloadBytes[i] = 'x'
		}
		dataPayload := string(dataPayloadBytes)

		// Insert records for each map.
		err = db.Update(func(tx *buntdb.Tx) error {
			for _, mapName := range mapNames {
				for i := 0; i < numEntries; i++ {
					// Construct key using map name as prefix.
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
			val, err := tx.Get("bunt_map3:key-60000")
			if err != nil {
				return err
			}
			// Unmarshal the JSON record.
			return json.Unmarshal([]byte(val), &record)
		})
		if err != nil {
			log.Fatal(err)
		}
		if record.ID != "bunt_map3-60000" {
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

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "persist":
			runPersist()
		case "bolt":
			runBoltDB()
		case "buntdb":
			runBuntDB()
		default:
			log.Fatal("Unknown db")
		}
	} else {
		runPersist()
		println()
		runBoltDB()
		println()
		runBuntDB()
	}
}
