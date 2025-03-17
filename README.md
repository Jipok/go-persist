# go-persist

A high-performance, type-safe, persisted key-value store for Go that combines the speed of in-memory maps with the durability of persistent storage.

[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/Jipok/go-persist.svg)](https://pkg.go.dev/github.com/Jipok/go-persist)

## 🤔 Why Another Key-Value Store?

At first glance, creating yet another embedded key-value database in Go seems redundant. Indeed, popular solutions like Bolt, BuntDB, Badger, or Pebble already exist, each having niche strengths. 

**However, existing solutions fall short for common Go application patterns**:

- **Manual Serialization Overhead**: Most existing databases represent values and keys as raw `[]byte` or `string`. To deal with structured data (e.g., Go structs), you repetitively marshal/unmarshal data (typically via JSON), incurring CPU and GC overhead and complicating your code.

- **Performance vs Persistence Dilemma**: High-performance concurrent solutions (`sync.Map` or mutex-protected `map`) lack persistence. Persistent databases trade convenience for speed and require complex caching logic to accelerate operations.

- **Complexity of Custom Caching Layers**: 
  - You often have two sources of truth (cache/in-memory and disk), making synchronization error-prone. 
  - Application logic becomes polluted by persistence management. 
  - State debugging and consistency across shutdowns/restarts requires significant effort.

**Go-persist aims to deliver the exact sweet spot:**
- 🚀 **Performance:** Near-native `sync.Map` performance for structured data.
- 🛡 **Safety:** Native Go types with transparent persistence.
- 🔋 **Convenience:** Simple, intuitive map-like APIs requiring no manual serialization.
- ⚙️ **Flexibility:** Explicit control over persistence guarantees (async, immediate, fsync).

📖 [More details on design considerations and trade-offs](https://github.com/Jipok/go-persist?tab=readme-ov-file#-design-trade-offs)

---

## 🧪 Performance Benchmarks Summary

*(Intel N100 Quad-Core @3.4GHz, Linux environment)*

*1M struct operations (150 goroutines), 100K items dataset*

| Solution           | Operations/sec | ns/op | File Size |
|--------------------|----------------|-------|-----------|
| go-persist `Async` | 7,117,079      | 140   | 6.07 MB   |
| sync.Map           | 5,509,706      | 181   | N/A       |
| map+RWMutex        | 2,532,314      | 394   | N/A       |
| go-persist `Sync`  | 1,463,708      | 683   | 6.07 MB   |
| buntdb             | 251,218        | 3980  | 11.15 MB  |
| bolt       `NoSync`| 181,481        | 5510  | 24.00 MB  |

Check detailed benchmarks and comparisons in the separate benchmark docs:

- [Operation-Intensive Benchmark](https://github.com/Jipok/go-persist/tree/master/benchmark)
- [Load (file loading, dataset scaling) Benchmark](https://github.com/Jipok/go-persist/tree/master/benchmark-load)

## 📦 Installation

```bash
go get github.com/Jipok/go-persist
```

## 🚀 Quick Start Example

```go
package main

import (
	"fmt"
	"log"

	"github.com/Jipok/go-persist"
)

type User struct {
    Name  string
    Email string
    Age   int
}

func main() {
    // Open a single persistent map in one call
    users, err := persist.OpenSingleMap[User]("users.db")
    if err != nil {
        log.Fatal(err)
    }
    defer users.Store.Close()

    // Store a user with balanced performance/durability
    users.Set("alice", User{
        Name: "Alice Smith",
        Email: "alice@example.com",
        Age: 28,
    })

    // Retrieve a user
    john, ok := users.Get("alice")
    if !ok {
        log.Fatal("User not found")
    }
    fmt.Printf("User: %+v\n", john)

    // Atomically update a user's age
    users.Update("alice", func(upd *persist.Update[User]) {
        if !upd.Exists {
            upd.Cancel() // Don't do anything if user doesn't exist
            return
        }
        // Modify the value directly
        upd.Value.Age++
    })

    // Count users
    fmt.Printf("Total users: %d\n", users.Size())

    // Iterate through all users
    users.Range(func(key string, value User) bool {
        fmt.Printf("Key: %s, User: %+v\n", key, value)
        return true // continue iteration
    })

    // Delete a user
    users.Delete("alice")
}
```

## 🛠️ API Examples

<details><summary>Using Multiple Typed Maps Together</summary>

```go
package main

import (
    "log"
    "time"
    "github.com/Jipok/go-persist"
)

type User struct {
    Name  string
    Age   int
}

type Product struct {
    Name  string
    Price float64
}

type Session struct {
    UserID     string
    Expiration int64
}

func main() {
    store := persist.New()
    defer store.Close()

    // Create typed maps for different entity types
    users, _ := persist.Map[User](store, "users")
    products, _ := persist.Map[Product](store, "products")
    sessions, _ := persist.Map[Session](store, "sessions")

    // Create or load store file
    err := store.Open("app.db")
    if err != nil {
        log.Fatal(err)
    }

    // Set up automatic compaction
    store.StartAutoShrink(time.Minute, 1.8)

    // Use each map independently
    users.Set("u1", User{Name: "Admin", Age: 35})
    products.Set("p1", Product{Name: "Widget", Price: 19.99})
    sessions.SetAsync("sess123", Session{UserID: "u1", Expiration: 1718557123})
}
```
</details>

<details><summary>PersistMap Methods</summary>

```go
// Retrieve data
value, exists := myMap.Get("key")

// Store data with different durability options
myMap.SetAsync("key", value)         // High performance, background persistence
myMap.Set("key", value)              // Balanced performance and durability
err := myMap.SetFSync("key", value)  // Maximum durability with fsync

// Delete data
myMap.DeleteAsync("key")             // Background delete
myMap.Delete("key")                  // Immediate WAL write
err := myMap.DeleteFSync("key")      // With fsync for maximum durability

// Atomic updates with different durability levels
newVal, existed := myMap.UpdateAsync("key", func(upd *persist.Update[T]) {
    // Modify upd.Value directly (default action is "set")
    // Or explicitly call:
    // upd.Set(newValue)    // to update the value
    // upd.Delete()         // to delete the key
    // upd.Cancel()         // to keep original value unchanged
})

newVal, existed := myMap.Update("key", func(upd *persist.Update[T]) {
    // Same options as above
})

newVal, existed, err := myMap.UpdateFSync("key", func(upd *persist.Update[T]) {
    // Same options as above
})

// Get number of items
count := myMap.Size()

// Iterate through all items
myMap.Range(func(key string, value ValueType) bool {
    // Process each item
    return true // return true to continue, false to stop
})
```

</details>

<details><summary>Using the Basic Store API</summary>

```go
type Config struct {
    Debug          bool
    MaxConnections int
}

func main() {
    store := persist.New()
    err := store.Open("app.db")
    if err != nil {
        log.Fatal(err)
    }
    defer store.Close()

    // Store configuration directly
    err = store.Set("system_config", Config{
        Debug:          true,
        MaxConnections: 100,
    })
    if err != nil {
        log.Fatal(err)
    }

    config, err := persist.Get[Config](store, "system_config")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Config: Debug=%v, MaxConnections=%d\n", config.Debug, config.MaxConnections)

    // When you need to update the config
    config.MaxConnections = 200
    err = store.Set("system_config", config)
    if err != nil {
        log.Fatal(err)
    }
}
```
</details>

<details><summary>WAL Management and Monitoring</summary>

```go
// Force immediate durability of all data
if err := store.FSyncAll(); err != nil {
    log.Fatal("Failed to sync data to disk:", err)
}

// Get statistics about the store
activeKeys, walRecords := store.Stats()
fmt.Printf("Active keys: %d, WAL records: %d, Ratio: %.2f\n", 
    activeKeys, walRecords, float64(walRecords)/float64(activeKeys))

// Manually compact the WAL file to reclaim space
if err := store.Shrink(); err != nil {
    log.Fatal(err)
}

// Or set up automatic compaction when record count exceeds 2x the active keys
store.StartAutoShrink(1*time.Minute, 2.0) // Check ratio every minute
```
</details>


---

### 🚩 Performance Control

`go-persist` lets you choose your performance and durability trade-off explicitly:


| Method                         | Performance | Persistence Guarantee               |
|--------------------------------|-------------|-------------------------------------|
| `.SetAsync()` | Highest 🚀        | Background persistence (potential loss on app crash)|
| `.Set()`           | Balanced      | Immediate WAL (application failure safe, OS failure risk)|
| `.SetFSync()` | Lowest 🔐         | Immediate WAL+fsync (fully durable, failure-proof)|

<details><summary>📚 Details</summary>

---

### Durability Levels

1. **Async Methods** (`SetAsync`, `DeleteAsync`, `UpdateAsync`): Highest performance with deferred persistence.
   - Updates are applied in-memory immediately
   - Changes are flushed to disk by a background process
   - Best for high-throughput scenarios where occasional data loss on crashes is acceptable

2. **Immediate Methods** (`Set`, `Delete`, `Update`): Balanced performance with immediate WAL updates.
   - Updates are applied in-memory and written to WAL immediately
   - Safe against application crashes, but susceptible to system crashes
   - Good for most typical use cases

3. **FSync Methods** (`SetFSync`, `DeleteFSync`, `UpdateFSync`): Maximum durability with fsync guarantee.
   - Updates are written to WAL and flushed to physical disk with fsync
   - Safe against both application and system crashes
   - Use when data integrity is critical
   - See [Design Trade-offs](https://github.com/Jipok/go-persist?tab=readme-ov-file#-design-trade-offs)

### Configuring Sync Interval

The sync interval controls:
* When batched `Async` operations are written to the WAL file
* When regular `Set` operations are synced from OS page cache to physical disk

```go
// Get the current sync interval
interval := store.GetSyncInterval()

// Set a custom sync interval
store.SetSyncInterval(500 * time.Millisecond) // More frequent syncing
// or
store.SetSyncInterval(1 * time.Second)  // Default
// or
store.SetSyncInterval(10 * time.Minute) // Minimal disk activity
```

Adjusting the sync interval lets you fine-tune the trade-off between performance and durability:

- **Short intervals** (milliseconds to second): Reduce potential data loss window but cause more frequent disk activity
- **Medium intervals** (seconds): Good balance for most applications
- **Long intervals** (minutes to hours): Minimize disk activity and extend SSD/HDD lifespan, but with larger potential data loss windows in case of crashes

With very long intervals, `Async` operations will cause practically no disk writes during normal operation, making this option excellent for conserving storage device lifespan when persistence is mainly needed for planned shutdowns rather than crash recovery.

For the `Set` method, even with a very long sync interval, changes are initially written to the OS page cache. The system itself will eventually flush these changes to disk (i.e., perform an fsync) according to its own caching policies. On Linux, by default:
* The parameter `/proc/sys/vm/dirty_writeback_centisecs` is typically set to 500 (≈5 seconds), meaning the kernel scans for dirty pages and may flush them every ~5 seconds.
* The parameter `/proc/sys/vm/dirty_expire_centisecs` is usually around 3000 (≈30 seconds), so pages older than ~30 seconds are forced to be written to disk.

---
</details>

## 📋 Human-readable and writable WAL format

```bash
go-persist 1                                          # Version header
S key1                                                # Set operation for key1
{"Name":"Alice","Age":30,"Email":"alice@example.com"} # Value
S key2                                                # Set operation for key2
"some plain string"                                   # Value
D key1                                                # Delete operation for key1
                                                      # Empty string for delete op
S key2                                                # New version of key2
"some another plain string"                           # Updated value

```

- `S`: Set an operation with a valid JSON payload
- `D`: Delete the key
- Easy to inspect and debug without special tools


## 📌 Intended Use Cases

- Development and rapid prototyping: simple setup without boilerplate.
- Configuration storage: typed data, effortlessly saved to disk.
- Local persistent caching layer: structured data requiring fast, concurrent access.
- Applications with moderate data volume (up to a few GB of RAM).

## 🚧 Design Trade-offs

**Human-Readable Format vs Checksums**
- The WAL format prioritizes human readability and debuggability
- No built-in checksums or CRCs to validate file integrity
- If you need stronger corruption detection, consider using this on a filesystem with checksumming (like ZFS, btrfs) and RAID
- Can detect incomplete format entries from crashes but can't detect bitrot or partial corruption within a syntactically valid entry

**Memory-First Approach**
- All data is kept in memory for maximum performance
- Not suitable for datasets larger than available RAM
- Provides map-like access patterns rather than database query capabilities

### Performance and Scale Considerations
- Tested with datasets up to ~1GB of real data
- WAL file grows unbounded until `Shrink()` is called
- No complex recovery mechanisms, distributed capabilities, or transaction isolation
- Memory usage scales linearly with dataset size, with reasonable overhead compared to raw data

For applications requiring complex queries, distributed access, full ACID compliance, strong data integrity guarantees, protection against hardware failures, or datasets larger than available memory, a traditional database system would be more appropriate.
