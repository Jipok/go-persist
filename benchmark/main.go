package main

import (
	"encoding/json"
	"fmt" // Используем для форматированного вывода
	"math/rand"
	"os"
	"strconv"
	"sync"

	"github.com/Jipok/go-persist"
	"github.com/boltdb/bolt"
	"github.com/tidwall/buntdb"
	"github.com/tidwall/lotsa"
)

// TestStruct – структура для бенчмарков
type TestStruct struct {
	Field1 int    `json:"field1"`
	Field2 string `json:"field2"`
}

// Helper function to print file size in MB
func printFileSize(filename string) {
	info, err := os.Stat(filename)
	if err != nil {
		fmt.Printf("os.Stat %s: %v\n", filename, err)
		return
	}
	sizeMB := float64(info.Size()) / (1024 * 1024)
	fmt.Printf("%s: %.2f MB\n", filename, sizeMB)
}

var prePopCount int
var benchOps int
var goroutines int
var writePerc int

/////////////////////////////////////////////////////////////////////////////////////////
// Benchmark FUNCTIONS: STRUCTS
/////////////////////////////////////////////////////////////////////////////////////////

// Benchmark for map+RWMutex (structs)
func benchmarkMapRWMutexStructs() {
	var m = make(map[string]TestStruct)
	var rwMutex sync.RWMutex

	// --- Pre-population phase ---
	for i := 0; i < prePopCount; i++ {
		key := strconv.Itoa(i)
		// Pre-fill with an example struct
		m[key] = TestStruct{
			Field1: i,
			Field2: "example struct",
		}
	}

	// --- Benchmark phase: concurrent random reads/writes ---
	fmt.Print("map+RWMutex  ")
	lotsa.Ops(benchOps, goroutines, func(i, thread int) {
		// Select a random key (from pre-populated keys)
		key := strconv.Itoa(rand.Intn(prePopCount))
		if rand.Intn(100) < writePerc {
			// Write operation
			ts := TestStruct{
				Field1: i,
				Field2: "updated struct",
			}
			rwMutex.Lock() // Lock for writing
			m[key] = ts
			rwMutex.Unlock() // Unlock after update
		} else {
			// Read operation
			rwMutex.RLock()   // Acquire read lock
			_ = m[key]        // Discard result
			rwMutex.RUnlock() // Release read lock
		}
	})
}

// Benchmark for sync.Map (structs)
func benchmarkSyncMapStructs() {
	var sMap sync.Map

	// --- Pre-population phase ---
	for i := 0; i < prePopCount; i++ {
		key := strconv.Itoa(i)
		sMap.Store(key, TestStruct{
			Field1: i,
			Field2: "example struct",
		})
	}

	// --- Benchmark phase ---
	fmt.Print("sync.Map     ")
	lotsa.Ops(benchOps, goroutines, func(i, thread int) {
		key := strconv.Itoa(rand.Intn(prePopCount))
		if rand.Intn(100) < writePerc {
			// Write operation
			ts := TestStruct{
				Field1: i,
				Field2: "updated struct",
			}
			sMap.Store(key, ts)
		} else {
			// Read operation
			if value, ok := sMap.Load(key); ok {
				_ = value.(TestStruct)
			}
		}
	})
}

// Benchmark for go-persist (structs)
func benchmarkPersistStructs() {
	os.Remove("persist.db1")
	persistStore, err := persist.Open("persist.db1")
	if err != nil {
		panic(err)
	}
	// Create a persistent map for TestStruct
	persistStructMap, _ := persist.Map[TestStruct](persistStore, "test_struct")

	// --- Pre-population phase ---
	for i := 0; i < prePopCount; i++ {
		key := strconv.Itoa(i)
		persistStructMap.Set(key, TestStruct{
			Field1: i,
			Field2: "example struct",
		})
	}
	// Flush pre-populated data
	persistStructMap.Flush()
	persistStore.Flush()

	// --- Benchmark phase ---
	fmt.Print("go-persist   ")
	lotsa.Ops(benchOps, goroutines, func(i, thread int) {
		key := strconv.Itoa(rand.Intn(prePopCount))
		if rand.Intn(100) < writePerc {
			// Write operation
			persistStructMap.Set(key, TestStruct{
				Field1: i,
				Field2: "updated struct",
			})
		} else {
			// Read operation
			val, ok := persistStructMap.Get(key)
			if !ok {
				panic(err)
			}
			_ = val
		}
	})
	persistStore.Close()
}

// Benchmark for BuntDB (structs with JSON serialization)
func benchmarkBuntDBStructs() {
	os.Remove("buntdb.db1")
	buntDB, err := buntdb.Open("buntdb.db1")
	if err != nil {
		panic(err)
	}

	// --- Pre-population phase ---
	for i := 0; i < prePopCount; i++ {
		key := strconv.Itoa(i)
		ts := TestStruct{
			Field1: i,
			Field2: "example struct",
		}
		data, err := json.Marshal(ts) // JSON serialization
		if err != nil {
			panic(err)
		}
		err = buntDB.Update(func(tx *buntdb.Tx) error {
			_, _, err := tx.Set(key, string(data), nil)
			return err
		})
		if err != nil {
			panic(err)
		}
	}

	// --- Benchmark phase ---
	fmt.Print("buntdb       ")
	lotsa.Ops(benchOps, goroutines, func(i, thread int) {
		key := strconv.Itoa(rand.Intn(prePopCount))
		if rand.Intn(100) < writePerc {
			// Write operation
			ts := TestStruct{
				Field1: i,
				Field2: "updated struct",
			}
			data, err := json.Marshal(ts)
			if err != nil {
				panic(err)
			}
			err = buntDB.Update(func(tx *buntdb.Tx) error {
				_, _, err := tx.Set(key, string(data), nil)
				return err
			})
			if err != nil {
				panic(err)
			}
		} else {
			// Read operation
			err = buntDB.View(func(tx *buntdb.Tx) error {
				val, err := tx.Get(key)
				if err != nil {
					return err
				}
				var ts TestStruct
				// JSON deserialization
				return json.Unmarshal([]byte(val), &ts)
			})
			if err != nil {
				panic(err)
			}
		}
	})
	buntDB.Close()
}

// Benchmark for Bolt (structs with JSON serialization)
func benchmarkBoltStructs() {
	os.Remove("bolt.db1")
	db, err := bolt.Open("bolt.db1", 0600, nil)
	if err != nil {
		panic(err)
	}
	db.NoSync = true

	// Create bucket "bench_struct" if not exists
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("bench_struct"))
		return err
	})
	if err != nil {
		panic(err)
	}

	// --- Pre-population phase ---
	for i := 0; i < prePopCount; i++ {
		key := strconv.Itoa(i)
		ts := TestStruct{
			Field1: i,
			Field2: "example struct",
		}
		data, err := json.Marshal(ts) // JSON serialization
		if err != nil {
			panic(err)
		}
		err = db.Update(func(tx *bolt.Tx) error {
			bucket := tx.Bucket([]byte("bench_struct"))
			return bucket.Put([]byte(key), data)
		})
		if err != nil {
			panic(err)
		}
	}

	// --- Benchmark phase ---
	fmt.Print("bolt         ")
	lotsa.Ops(benchOps, goroutines, func(i, thread int) {
		key := strconv.Itoa(rand.Intn(prePopCount))
		if rand.Intn(100) < writePerc {
			// Write operation
			ts := TestStruct{
				Field1: i,
				Field2: "updated struct",
			}
			data, err := json.Marshal(ts)
			if err != nil {
				panic(err)
			}
			err = db.Update(func(tx *bolt.Tx) error {
				bucket := tx.Bucket([]byte("bench_struct"))
				return bucket.Put([]byte(key), data)
			})
			if err != nil {
				panic(err)
			}
		} else {
			// Read operation
			err = db.View(func(tx *bolt.Tx) error {
				bucket := tx.Bucket([]byte("bench_struct"))
				data := bucket.Get([]byte(key))
				var ts TestStruct
				// JSON deserialization
				return json.Unmarshal(data, &ts)
			})
			if err != nil {
				panic(err)
			}
		}
	})
	db.Close()
}

/////////////////////////////////////////////////////////////////////////////////////////
// Benchmark FUNCTIONS: STRINGS
/////////////////////////////////////////////////////////////////////////////////////////

// Benchmark for map+RWMutex (strings)
func benchmarkMapRWMutexStrings() {
	var m = make(map[string]string)
	var rwMutex sync.RWMutex
	stringValue := "gq2ip4;9209;4fm2d1d3DJ138D2L38\t2FP2938FP238HFP2H  FDAUWF1\t2"

	// --- Pre-population phase ---
	for i := 0; i < prePopCount; i++ {
		key := strconv.Itoa(i)
		m[key] = stringValue
	}

	// --- Benchmark phase ---
	fmt.Print("map+RWMutex  ")
	lotsa.Ops(benchOps, goroutines, func(i, thread int) {
		key := strconv.Itoa(rand.Intn(prePopCount))
		if rand.Intn(100) < writePerc {
			// Write operation
			rwMutex.Lock()
			m[key] = stringValue + " updated"
			rwMutex.Unlock()
		} else {
			// Read operation
			rwMutex.RLock()
			_ = m[key]
			rwMutex.RUnlock()
		}
	})
}

// Benchmark for sync.Map (strings)
func benchmarkSyncMapStrings() {
	var sMap sync.Map
	stringValue := "gq2ip4;9209;4fm2d1d3DJ138D2L38\t2FP2938FP238HFP2H  FDAUWF1\t2"

	// --- Pre-population phase ---
	for i := 0; i < prePopCount; i++ {
		key := strconv.Itoa(i)
		sMap.Store(key, stringValue)
	}

	// --- Benchmark phase ---
	fmt.Print("sync.Map     ")
	lotsa.Ops(benchOps, goroutines, func(i, thread int) {
		key := strconv.Itoa(rand.Intn(prePopCount))
		if rand.Intn(100) < writePerc {
			// Write operation
			sMap.Store(key, stringValue+" updated")
		} else {
			// Read operation
			if value, ok := sMap.Load(key); ok {
				_ = value.(string)
			}
		}
	})
}

// Benchmark for go-persist (strings)
func benchmarkPersistStrings() {
	os.Remove("persist.db2")
	persistStore, err := persist.Open("persist.db2")
	if err != nil {
		panic(err)
	}
	persistMap, _ := persist.Map[string](persistStore, "test")
	stringValue := "gq2ip4;9209;4fm2d1d3DJ138D2L38\t2FP2938FP238HFP2H  FDAUWF1\t2"

	// --- Pre-population phase ---
	for i := 0; i < prePopCount; i++ {
		key := strconv.Itoa(i)
		persistMap.Set(key, stringValue)
	}
	persistStore.Flush()

	// --- Benchmark phase ---
	fmt.Print("go-persist   ")
	lotsa.Ops(benchOps, goroutines, func(i, thread int) {
		key := strconv.Itoa(rand.Intn(prePopCount))
		if rand.Intn(100) < writePerc {
			// Write operation
			persistMap.Set(key, stringValue+" updated")
		} else {
			// Read operation
			val, ok := persistMap.Get(key)
			if !ok {
				panic(err)
			}
			_ = val
		}
	})
	persistStore.Close()
}

// Benchmark for BuntDB (strings)
func benchmarkBuntDBStrings() {
	os.Remove("test.buntdb")
	buntDB, err := buntdb.Open("test.buntdb")
	if err != nil {
		panic(err)
	}
	stringValue := "gq2ip4;9209;4fm2d1d3DJ138D2L38\t2FP2938FP238HFP2H  FDAUWF1\t2"

	// --- Pre-population phase ---
	for i := 0; i < prePopCount; i++ {
		key := strconv.Itoa(i)
		err := buntDB.Update(func(tx *buntdb.Tx) error {
			_, _, err := tx.Set(key, stringValue, nil)
			return err
		})
		if err != nil {
			panic(err)
		}
	}

	// --- Benchmark phase ---
	fmt.Print("buntdb       ")
	lotsa.Ops(benchOps, goroutines, func(i, thread int) {
		key := strconv.Itoa(rand.Intn(prePopCount))
		if rand.Intn(100) < writePerc {
			// Write operation
			err := buntDB.Update(func(tx *buntdb.Tx) error {
				_, _, err := tx.Set(key, stringValue+" updated", nil)
				return err
			})
			if err != nil {
				panic(err)
			}
		} else {
			// Read operation
			err := buntDB.View(func(tx *buntdb.Tx) error {
				_, err := tx.Get(key)
				return err
			})
			if err != nil {
				panic(err)
			}
		}
	})
	buntDB.Close()
}

// Benchmark for Bolt (strings)
func benchmarkBoltStrings() {
	os.Remove("bolt.db2")
	db, err := bolt.Open("bolt.db2", 0600, nil)
	if err != nil {
		panic(err)
	}
	db.NoSync = true

	// Create bucket "bench" if not exists
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("bench"))
		return err
	})
	if err != nil {
		panic(err)
	}
	stringValue := "gq2ip4;9209;4fm2d1d3DJ138D2L38\t2FP2938FP238HFP2H  FDAUWF1\t2"

	// --- Pre-population phase ---
	for i := 0; i < prePopCount; i++ {
		key := strconv.Itoa(i)
		err := db.Update(func(tx *bolt.Tx) error {
			bucket := tx.Bucket([]byte("bench"))
			return bucket.Put([]byte(key), []byte(stringValue))
		})
		if err != nil {
			panic(err)
		}
	}

	// --- Benchmark phase ---
	fmt.Print("bolt         ")
	lotsa.Ops(benchOps, goroutines, func(i, thread int) {
		key := strconv.Itoa(rand.Intn(prePopCount))
		if rand.Intn(100) < writePerc {
			// Write operation
			err := db.Update(func(tx *bolt.Tx) error {
				bucket := tx.Bucket([]byte("bench"))
				return bucket.Put([]byte(key), []byte(stringValue+" updated"))
			})
			if err != nil {
				panic(err)
			}
		} else {
			// Read operation
			err := db.View(func(tx *bolt.Tx) error {
				bucket := tx.Bucket([]byte("bench"))
				_ = bucket.Get([]byte(key))
				return nil
			})
			if err != nil {
				panic(err)
			}
		}
	})
	db.Close()
}

/////////////////////////////////////////////////////////////////////////////////////////
// Main
/////////////////////////////////////////////////////////////////////////////////////////

func main() {
	lotsa.Output = os.Stdout
	// lotsa.MemUsage = true // TODO Why wrong values for sync.Map?

	// Set benchmark constants
	prePopCount = 100000 // keys for pre-population
	benchOps = 100000    // number of operations during benchmark phase
	goroutines = 150     // number of goroutines for lotsa.Ops (actual load)
	writePerc = 20       // write operations ratio (20% write, 80% read)

	fmt.Println("===== Benchmarking: Structs =====")
	benchmarkPersistStructs()
	benchmarkSyncMapStructs()
	benchmarkMapRWMutexStructs()
	benchmarkBuntDBStructs()
	benchmarkBoltStructs()

	fmt.Println("\n----- File sizes (MB) -----")
	printFileSize("persist.db1")
	printFileSize("buntdb.db1")
	printFileSize("bolt.db1")

	fmt.Println("\n===== Benchmarking: Strings =====")
	benchmarkPersistStrings()
	benchmarkSyncMapStrings()
	benchmarkMapRWMutexStrings()
	benchmarkBuntDBStrings()
	benchmarkBoltStrings()

	fmt.Println("\n----- File sizes (MB) -----")
	printFileSize("persist.db2")
	printFileSize("test.buntdb")
	printFileSize("bolt.db2")

	// Clean up benchmark files
	os.Remove("persist.db1")
	os.Remove("buntdb.db1")
	os.Remove("bolt.db1")
	os.Remove("persist.db2")
	os.Remove("test.buntdb")
	os.Remove("bolt.db2")
}
