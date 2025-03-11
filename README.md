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

The result is a solution that combines the best of both worlds:

- Type-safe semantics: no manual marshaling/unmarshaling in your code
- Near-native concurrent performance: on par with `sync.Map`
- Human-readable persistent storage: JSON-based WAL (Write-Ahead Logs), easy to inspect or debug
- Compact and predictable file sizes compared to traditional approaches

Ultimately, `go-persist` was born from real-world pain point, eliminating the unnecessary layers of complexity, duplication of logic, and serialization overhead endemic to traditional solutions.

## Performance

*Benchmark: 1M struct operations over 150 threads, after 100k prefill*
| Solution           | Operations/sec | ns/op | File Size |
|--------------------|----------------|-------|-----------|
| go-persist `Async` | 7,114,784      | 140   | 6.07 MB   |
| sync.Map           | 5,533,168      | 180   | N/A       |
| map+RWMutex        | 2,132,890      | 468   | N/A       |
| go-persist `Set`   | 1,351,765      | 739   | 6.07 MB   |
| buntdb             | 240,207        | 4163  | 8.41 MB   |
| bolt       `NoSync`| 179,476        | 5571  | 24.00 MB  |

Additional benchmarks and detailed results are [available in the repository](https://github.com/Jipok/go-persist/blob/master/benchmark/result.txt). Benchmarks were carried out on a modest system (Intel N100 with Void Linux). The results consistently show that go-persist is competitive with in-memory maps while providing persistent storage and maintaining a relatively small file size.

## Human-readable and writable Write-Ahead Log format:

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

## Installation

```bash
go get github.com/Jipok/go-persist
```

## Usage Examples

### Using PersistMap (Type-Safe API)

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
    // Create or open store
    store, err := persist.Open("users.db")
    if err != nil {
        log.Fatal(err)
    }
    defer store.Close()

    // Compact the store periodically to reclaim space
    if err := store.Shrink(); err != nil {
        log.Fatal(err)
    }

    // Create or load a typed map
    users, err := persist.Map[User](store, "users")
    if err != nil {
        log.Fatal(err)
    }

    // Store a user (with different durability options)

    // Option 1: High Performance (background flush - fastest)
    users.SetAsync("john", User{
        Name: "John Doe",
        Email: "john@example.com",
        Age: 30,
    })

    // Recommended
    // Option 2: Immediate WAL Write (balanced durability)
    users.Set("alice", User{
        Name: "Alice Smith",
        Email: "alice@example.com",
        Age: 28,
    })

    // Option 3: Maximum Durability (with fsync - slowest)
    err = users.SetFSync("bob", User{
        Name: "Bob Johnson",
        Email: "bob@example.com",
        Age: 35,
    })
    if err != nil {
        log.Fatal(err)
    }

    // Retrieve a user
    john, ok := users.Get("john")
    if !ok {
        log.Fatal("User not found")
    }
    fmt.Printf("User: %+v\n", john)

    // Delete a user (also with durability options)
    users.DeleteAsync("john")  // Background delete (fastest)
    users.Delete("alice")      // Immediate WAL write

    err = users.DeleteFSync("bob")  // Maximum durability with fsync
    if err != nil {
        log.Fatal(err)
    }

    // Atomic updates
    // There are also UpdateAsync and UpdateFSync
    users.Update("sam", func(current User, exists bool) (User, bool) {
        if !exists {
            // Create new user if doesn't exist
            return User{Name: "Sam Wilson", Age: 25}, false
        }
        // Modify existing user
        current.Age++
        return current, false // Return updated value, don't delete
    })

    count := users.Size()
    fmt.Printf("User count: %d\n", count)

    // Iterate through all users
    users.Range(func(key string, value User) bool {
        fmt.Printf("Key: %s, User: %+v\n", key, value)
        return true // continue iteration
    })
}
```

### Multiple Maps in One Store

```go
// Single store can contain multiple typed maps
store, err := persist.Open("app.db")
if err != nil {
    log.Fatal(err)
}
defer store.Close()

// Create typed maps for different entity types
users, err := persist.Map[User](store, "users")
products, err := persist.Map[Product](store, "products")
sessions, err := persist.Map[Session](store, "sessions")

// Use independently
users.Set("u1", User{Name: "Admin"})
products.Set("p1", Product{Name: "Widget", Price: 19.99})
```

### Using the Basic Store API

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

    // Compact the store periodically to reclaim space
    if err := store.Shrink(); err != nil {
        log.Fatal(err)
    }

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

### Configuring Sync Interval

The sync interval controls:
* When batched Async operations are written to the WAL file
* When regular Set operations are synced from OS page cache to physical disk

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

## When to Use

- Applications requiring persistence with minimal overhead
- Working with typed data structures
- When data inspection and debugging are important
- For heavy workloads (consider background flush interval)
- When atomic update operations are needed

## Limitations

- Not designed for datasets larger than available memory
- No support for complex queries or secondary indexes
- WAL file grows until `Shrink()` is called