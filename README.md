# go-persist

A high-performance, type-safe, persisted key-value store for Go, leveraging generics and WAL-based persistence.

## Motivation

At first glance, building yet another key-value store in Go might seem redundant. There are plenty of popular solutions already available — Bolt, BuntDB, Badger, Pebble, Bbolt and others, each with its particular strengths. However, my experience has consistently demonstrated a fundamental mismatch between what's readily available and what many Go applications actually need.

Most existing databases approach persistence from a storage-first perspective: they store raw keys and values as byte sequences or strings, requiring the developer to manage data serialization and deserialization manually. While generic and flexible, this approach introduces noticeable overhead: your application code has to continuously marshal and unmarshal data to and from complex structures (commonly JSON), wasting CPU cycles and making code less clear and maintainable.

To mitigate serialization overhead, developers often add a separate typed cache (like a `sync.Map` or a `map` guarded by mutexes) to their applications. However, this approach brings its own set of complexities:

- You now have two sources of truth, requiring careful synchronization to prevent stale data or race conditions.
- Persistence logic becomes complicated, cluttering your business codebase.
- Debugging and maintaining state across restarts becomes inherently more challenging.

I created `go-persist` because I wanted a better way: a persistent store as simple to use as an ordinary Go map, yet powerful enough to offer type-safe, high-performance concurrent operations without additional caching layer.

With the introduction of Generics in recent Go versions and the availability of advanced concurrent maps ([`xsync.Map`](https://github.com/puzpuzpuz/xsync?tab=readme-ov-file#map)), it became feasible to maintain type safety and near-native `sync.Map` performance without sacrificing persistence guarantees. Unlike traditional databases which serialize everything to strings or byte arrays, `go-persist` keeps data as native Go types in memory, automatically handling JSON serialization transparently only during persistence.

## Performance

*Benchmark: 1M struct operations over 150 goroutines, after 100k prefill*
| Solution           | Operations/sec | ns/op | File Size |
|--------------------|----------------|-------|-----------|
| go-persist `Async` | 7,117,079      | 140   | 6.07 MB   |
| sync.Map           | 5,509,706      | 181   | N/A       |
| map+RWMutex        | 2,532,314      | 394   | N/A       |
| go-persist `Sync`  | 1,463,708      | 683   | 6.07 MB   |
| buntdb             | 251,218        | 3980  | 11.15 MB  |
| bolt       `NoSync`| 181,481        | 5510  | 24.00 MB  |

Additional benchmarks and detailed results are [available in the repository](https://github.com/Jipok/go-persist/blob/master/benchmark/result.txt). Benchmarks were carried out on a modest system (Intel N100 with Void Linux). The results consistently show that go-persist is competitive with in-memory maps while providing persistent storage and maintaining a relatively small file size.

## Installation

```bash
go get github.com/Jipok/go-persist
```

## Quick Start

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
    john, ok := users.Get("john")
    if !ok {
        log.Fatal("User not found")
    }
    fmt.Printf("User: %+v\n", john)

    // Atomically update a user's age
    users.Update("john", func(upd *persist.Update[User]) {
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

### Using Multiple Maps in One Store

```go
package main

import (
    "log"
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
    users, err := persist.Map[User](store, "users")
    if err != nil {
        log.Fatal(err)
    }

    products, err := persist.Map[Product](store, "products")
    if err != nil {
        log.Fatal(err)
    }

    sessions, err := persist.Map[Session](store, "sessions")
    if err != nil {
        log.Fatal(err)
    }

    // Create or load store file
    err := persist.Open("app.db")
    if err != nil {
        log.Fatal(err)
    }

    // Compact the store to reclaim space
    if err := store.Shrink(); err != nil {
        log.Fatal(err)
    }

    // Use each map independently
    users.Set("u1", User{Name: "Admin", Age: 35})
    products.Set("p1", Product{Name: "Widget", Price: 19.99})
    sessions.SetAsync("sess123", Session{UserID: "u1", Expiration: 1718557123})
}
```

## Detailed API Usage

### PersistMap Methods

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

// Clean up resources
myMap.Free()
```

### Using the Basic Store API

For simple configuration or single-value storage:

```go
type Config struct {
    Debug          bool
    MaxConnections int
}

func main() {
    // Create or open store
    store, err := persist.Open("app.db")
    if err != nil {
        log.Fatal(err)
    }
    defer store.Close()

    // Store configuration directly
    err = store.Write("system_config", Config{
        Debug:          true,
        MaxConnections: 100,
    })
    if err != nil {
        log.Fatal(err)
    }

    // NOTE: store.Read reads the entire WAL and should primarily be
    // used for initial loading at program start, not frequent access
    var config Config
    err = store.Read("system_config", &config)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Config: Debug=%v, MaxConnections=%d\n", config.Debug, config.MaxConnections)

    // When you need to update the config
    config.MaxConnections = 200
    err = store.Write("system_config", config)
    if err != nil {
        log.Fatal(err)
    }
}
```

## Human-readable and writable `store.db` format

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

- `S key` - Set operation
- `D key` - Delete operation
- **Values**: Stored as standard JSON on the line after the operation

## Durability Levels

go-persist offers multiple durability options to balance performance and data safety:

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
   - See [Design Trade-offs](https://github.com/Jipok/go-persist#design-trade-offs)

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

## Design Considerations and Trade-offs

go-persist is designed with specific goals in mind: simplicity, type safety, and performance for embedded use cases. Understanding the following considerations will help you determine if it's the right fit for your needs:

### Intended Use Cases
- **Configuration storage**: Store application settings with typed access
- **Local caches with persistence**: Keep type-safe data across application restarts
- **Lightweight structured data storage**: For applications where a full database is overkill
- **High-throughput workloads**: With Async mode, can handle extremely high write volumes when immediate durability isn't critical
- **Development and prototyping**: Quick setup with minimal configuration
- **Small to medium data volumes**: Ideally under a few GB of data

### Design Trade-offs

**Human-Readable Format vs Checksums**
- The WAL format prioritizes human readability and debuggability
- No built-in checksums or CRCs to validate file integrity
- If you need stronger corruption detection, consider using this on a filesystem with checksumming (like ZFS, btrfs) or RAID
- Can detect incomplete format entries from crashes but can't detect bitrot or partial corruption within a syntactically valid entry

**Memory-First Approach**
- All data is kept in memory for maximum performance
- Not suitable for datasets larger than available RAM
- Provides map-like access patterns rather than database query capabilities

**Simplicity Over Advanced Features**
- Minimal codebase (<1000 lines) for easier review and understanding
- No complex recovery mechanisms, distributed capabilities, or transaction isolation
- `Shrink()` operation is blocking by design to keep implementation simple

### Performance and Scale Considerations
- Tested with datasets up to ~400MB of actual data
- WAL file grows unbounded until `Shrink()` is called
- `Shrink()` can cause brief (up to second) pauses; best called during low-activity periods
- Memory usage scales linearly with dataset size, with reasonable overhead compared to raw data

For applications requiring complex queries, distributed access, full ACID compliance, strong data integrity guarantees, protection against hardware failures, or datasets larger than available memory, a traditional database system would be more appropriate.
