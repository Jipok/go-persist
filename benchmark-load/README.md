# Load Performance Benchmark Results

This benchmark focuses on load time and scaling characteristics across different dataset sizes.

The measurements include file loading, initialization costs, and subsequent operations to evaluate how each solution handles increasingly larger datasets. Pure operational performance is covered in a [other benchmark](https://github.com/Jipok/go-persist/tree/master/benchmark).

## Dataset Details

- **Structure**: Each record (`ComplexRecord`) includes multiple string fields, nested metadata, and a 1KB data payload.
- **Key size**: 10–14 bytes.
- **Average record size**: 1215 ± 4 bytes.
- **Collections**: 5 separate collections ("maps").

**Dataset sizes tested**:

| Size   | Records per Collection | Total Records |
|--------|------------------------|---------------|
| Small  | 40,960                 | ~205K         |
| Medium | 81,920                 | ~410K         |
| Large  | 163,840                | ~820K         |

## Performance Results

### ⚡ Write Performance

| Solution | Small | Medium | Large | Scalability |
|----------|-------|--------|-------|-------------|
| **Badger**   | **1.41s** | 3.31s  | 7.78s | Linear      |
| **Persist**  | 1.47s | **2.95s**  | **5.87s** | Linear      |
| BuntDB   | 1.83s | 3.83s  | 7.59s | Linear      |
| Pebble   | 3.32s | 5.78s  | 11.17s | Linear      |
| VoidDB   | 5.54s | 11.78s | 25.38s | Linear      |
| BoltDB   | 5.31s | 10.09s | 261.08s | Exponential |

### 🔎 Single Record Lookup ("map-3-key-40000")

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| **BoltDB**   | **0.01s** | **0.01s**  | **0.01s** |
| **VoidDB**   | 0.03s | 0.02s  | 0.05s |
| Badger   | 0.07s | 0.12s  | 0.17s |
| Pebble   | 0.66s | 0.13s  | 0.18s |
| BuntDB   | 0.66s | 1.42s  | 3.24s |
| Persist  | 0.83s | 1.64s  | 3.53s |

BoltDB and VoidDB provide near-instant reads directly from disk without significant upfront loading. Badger and Pebble use partial loading strategies with good performance. Persist and BuntDB, designed for repeated efficient access, load data into memory initially, incurring a one-time startup cost.

### 📚 Batch Read (40K sequential records)

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| **Badger**   | **0.72s** | **1.17s**  | 3.60s |
| **Persist**  | 0.83s | 1.68s  | **3.55s** |
| BuntDB   | 0.88s | 1.69s  | 3.66s |
| VoidDB   | 1.75s | 3.17s  | 5.97s |
| Pebble   | 2.03s | 2.99s  | 4.81s |
| BoltDB   | 3.01s | 3.27s  | 3.79s |

Badger maintains strong batch read performance. But once loaded, go-persist and BuntDB provide good performance especially. BoltDB's disk-based lookups show consistent but slower performance across all sizes.

### 💾 Storage Efficiency

| Solution | Small (~205K) | Medium (~410K) | Large (~820K) | Compression |
|----------|---------------|----------------|---------------|-------------|
| **Badger**   | **237 MB** | **474 MB**  | **943 MB** | ✅ Yes |
| Persist  | 241 MB | 483 MB | 967 MB | ❌ No |
| BuntDB   | 249 MB | 492 MB | 986 MB | ❌ No |
| Pebble   | 258 MB | 486 MB | 974 MB | ✅ Yes |
| BoltDB   | 420 MB | 826 MB | 1636 MB | ❌ No |
| VoidDB   | 1122 MB | 2245 MB | 4490 MB | ❌ No |

Badger and Pebble use built-in data compression(Snappy), contributing to their excellent storage efficiency, especially with low-entropy data. With more realistic high-entropy data, the compression advantage is less dramatic but still gives these solutions a slight edge. Persist and BuntDB show comparable storage efficiency without compression mechanisms. BoltDB uses significantly more disk space, while VoidDB requires substantially more space than all other solutions.

This is an important consideration when selecting a solution for specific use cases: with low-entropy data (like JSON with highly repetitive structures and small data variations), the compression advantage of Badger and Pebble would be more pronounced.

### 📈 Memory Usage After Initial Load (single query)

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| **VoidDB**   | **0 bytes** | **0 bytes** | **0 bytes** |
| **BoltDB**   | 5.2 KB | 5.5 KB | 5.4 KB |
| Pebble   | 1.3 MB | 611.6 KB | 819.5 KB |
| Badger   | 85.7 MB | 85.8 MB | 85.9 MB |
| BuntDB   | 268.4 MB | 533.8 MB | 1.0 GB |
| Persist  | 301.8 MB | 602.8 MB | 1.2 GB |

VoidDB shows exceptionally low initial memory usage, followed by BoltDB's consistent minimal footprint. Badger shows consistent memory requirements regardless of dataset size. Persist and BuntDB use significant memory upfront to enable ultra-low-latency repeated access thereafter.

### 📈 Memory Usage After Batch Reads (40K reads)

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| **VoidDB**   | 181.7 KB | 1.6 MB | **90.7 KB** |
| **BoltDB**   | 481.5 KB | 1.4 MB | 657.9 KB |
| Pebble   | 627.0 KB | 1.7 MB | 1.4 MB |
| Badger   | 216.0 MB | 204.6 MB | 392.0 MB |
| BuntDB   | 329.1 MB | 594.2 MB | 1.1 GB |
| Persist  | 302.8 MB | 603.8 MB | 1.2 GB |

With high-entropy data, VoidDB shows remarkably low memory usage after batch operations, competing closely with BoltDB and Pebble. Badger consumes significant memory during batch operations but still less than the full in-memory solutions. Persist and BuntDB consume the most memory as expected from their design approach.

### 🔄 Sequential Open-Read-Close Cycles (1000 iterations)

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| **BoltDB**   | **0.05ms** | **0.05ms** | **0.06ms** |
| **VoidDB**  | 0.09ms | 0.08ms | 0.11ms |
| Pebble   | 15.28ms | 15.26ms | 17.06ms |
| BadgerDB  | 19.34ms | 19.47ms | 23.66ms |

This benchmark evaluates how quickly each solution can be opened, perform a single read operation, and then closed. This pattern is particularly important for short-lived processes, serverless functions, or applications that need to access data sporadically rather than maintain long-running connections.

BoltDB demonstrates exceptional performance in this pattern, requiring only 0.05ms on average to complete the entire open-read-close cycle. VoidDB also performs admirably in this scenario. Pebble and BadgerDB, despite their other advantages, have significantly higher overhead when frequently opened and closed.

## 📂 File Storage Performance (Binary Blobs 2-5MB each)

This benchmark tests how each solution handles storing and retrieving large binary files (2-5MB each), mimicking use cases like document or image storage.

| Solution | Write (100 files) | Write (1000 files) | Read (single file) | Memory (single read) | Storage Efficiency (1000 files) |
|----------|-------------------|-------------------|-------------------|---------------------|-------------------|
| **VoidDB**   | **0.87s** | **8.41s** | **0.01s** | 6.4 KB | 5255.59 MB |
| **Badger**   | 1.10s | 12.39s | 0.07s | 88.2 MB | **3444.60 MB** |
| BoltDB   | 1.59s | 30.52s | 0.01s | **3.0 KB** | 3460.61 MB |
| Pebble   | 3.06s | 73.64s | 0.11s | 12.1 MB | 3459.34 MB |

VoidDB delivers the fastest write speeds but uses 52% more storage space than competitors. Badger balances good performance with optimal storage efficiency. Pebble significantly lags in write performance, taking 8.7x longer than VoidDB.

## Conclusion

> [!NOTE]
> This benchmark focuses specifically on load characteristics, storage efficiency, and scaling properties rather than [operational performance](https://github.com/Jipok/go-persist/tree/master/benchmark).

- **Persist**: Excellent write and batch read performance, good for applications needing fast repeated access with reasonable memory availability
- **Badger**: Balanced performance across all operations with excellent storage efficiency; the most versatile solution
- **BoltDB**: Minimal memory footprint and exceptional open-read-close performance, ideal for serverless or sporadic access patterns
- **VoidDB**: Extremely low memory usage with excellent single-record performance, though storage inefficient
- **Pebble**: Good storage efficiency but generally middle-of-pack performance
- **BuntDB**: Similar performance profile to Persist but with slightly higher storage requirements