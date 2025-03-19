package main

import (
	"fmt"
	"log"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/dgraph-io/badger/v4"
	"github.com/goccy/go-json"
	"github.com/voidDB/voidDB"
	bolt "go.etcd.io/bbolt"
)

// sequentialOpenBolt measures 1000 sequential open-read operations for BoltDB.
func sequentialOpenBolt() {
	flushPageCache()
	start := time.Now()
	for i := 0; i < 1000; i++ {
		db, err := bolt.Open("bolt.db", 0666, nil)
		if err != nil {
			log.Fatal(err)
		}
		// Read one value from bucket "map3" with key "key-40000"
		err = db.View(func(tx *bolt.Tx) error {
			bucket := tx.Bucket([]byte("map3"))
			if bucket == nil {
				return fmt.Errorf("bucket map3 not found")
			}
			value := bucket.Get([]byte("key-40000"))
			if value == nil {
				return fmt.Errorf("key not found")
			}
			var record ComplexRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return err
			}
			if record.ID != "map3-40000" {
				return fmt.Errorf("Wrong value in BoltDB: %s", record.ID)
			}
			return nil
		})
		if err != nil {
			log.Fatal(err)
		}
		if err := db.Close(); err != nil {
			log.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	fmt.Printf("BoltDB 1000 sequential open-read: %.2fs (avg %.2f ms per iteration)\n",
		elapsed.Seconds(), float64(elapsed.Milliseconds())/1000.0)
}

// discardLogger is a no-op logger that implements pebble.Logger
type discardLogger struct{}

func (d discardLogger) Infof(format string, args ...interface{})  {}
func (d discardLogger) Fatalf(format string, args ...interface{}) {}

// sequentialOpenPebble measures 1000 sequential open-read operations for Pebble.
func sequentialOpenPebble() {
	flushPageCache()
	start := time.Now()
	for i := 0; i < 1000; i++ {
		db, err := pebble.Open("pebble.db", &pebble.Options{Logger: discardLogger{}})
		if err != nil {
			log.Fatal(err)
		}
		// Read one value with key "map3:key-40000"
		err = func() error {
			key := "map3:key-40000"
			value, closer, err := db.Get([]byte(key))
			if err != nil {
				return err
			}
			if closer != nil {
				closer.Close() // close the returned closer to free resources
			}
			var record ComplexRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return err
			}
			if record.ID != "map3-40000" {
				return fmt.Errorf("Wrong value in Pebble: %s", record.ID)
			}
			return nil
		}()
		if err != nil {
			log.Fatal(err)
		}
		if err := db.Close(); err != nil {
			log.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	fmt.Printf("Pebble 1000 sequential open-read: %.2fs (avg %.2f ms per iteration)\n",
		elapsed.Seconds(), float64(elapsed.Milliseconds())/1000.0)
}

// sequentialOpenBadger measures 1000 sequential open-read operations for BadgerDB.
func sequentialOpenBadger() {
	flushPageCache()
	start := time.Now()
	opts := badger.DefaultOptions("badger.db")
	opts.SyncWrites = false
	opts.Logger = nil
	for i := 0; i < 1000; i++ {
		db, err := badger.Open(opts)
		if err != nil {
			log.Fatal(err)
		}
		// Read one value with key "map3:key-40000"
		err = db.View(func(txn *badger.Txn) error {
			item, err := txn.Get([]byte("map3:key-40000"))
			if err != nil {
				return err
			}
			var record ComplexRecord
			if err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, &record)
			}); err != nil {
				return err
			}
			if record.ID != "map3-40000" {
				return fmt.Errorf("Wrong value in BadgerDB: %s", record.ID)
			}
			return nil
		})
		if err != nil {
			log.Fatal(err)
		}
		if err := db.Close(); err != nil {
			log.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	fmt.Printf("BadgerDB 1000 sequential open-read: %.2fs (avg %.2f ms per iteration)\n",
		elapsed.Seconds(), float64(elapsed.Milliseconds())/1000.0)
}

func sequentialOpenVoid() {
	const capacity = 1024 * 1024 * 1024 * 20 // 20GB
	const path = "void.db"
	start := time.Now()
	for i := 0; i < 1000; i++ {
		v, err := voidDB.OpenVoid(path, capacity)
		if err != nil {
			log.Fatal(err)
		}
		// Read one value from keyspace "map3" with key "key-40000"
		err = v.View(func(txn *voidDB.Txn) error {
			// Open a cursor for the "map3" keyspace
			cur, err := txn.OpenCursor([]byte("map3"))
			if err != nil {
				return err
			}
			val, err := cur.Get([]byte("key-40000"))
			if err != nil {
				return fmt.Errorf("VoidDB key not found: %v", err)
			}
			var record ComplexRecord
			if err := json.Unmarshal(val, &record); err != nil {
				return err
			}
			if record.ID != "map3-40000" {
				return fmt.Errorf("Wrong value in VoidDB: %s", record.ID)
			}
			return nil
		})
		if err != nil {
			log.Fatal(err)
		}
		if err := v.Close(); err != nil {
			log.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	fmt.Printf("VoidDB 1000 sequential open-read: %.2fs (avg %.2f ms per iteration)\n",
		elapsed.Seconds(), float64(elapsed.Milliseconds())/1000.0)
}
