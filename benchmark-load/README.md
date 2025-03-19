# Load Performance Benchmark Results

This benchmark focuses on load time and scaling characteristics across different dataset sizes.

The measurements include file loading, initialization costs, and subsequent operations to evaluate how each solution handles increasingly larger datasets. Pure operational performance is covered in a [other benchmark](https://github.com/Jipok/go-persist/tree/master/benchmark).

## Dataset Details

- **Structure**: Each record (`ComplexRecord`) includes multiple string fields, nested metadata, and a 1KB data payload.
- **Key size**: 10–14 bytes.
- **Average record size**: 1215 ± 4 bytes.
- **Collections**: 5 separate collections ("maps").

**Dataset sizes tested**:

| Size   | Records per Collection | Total Records |
|--------|------------------------|---------------|
| Small  | 40,960                 | ~205K         |
| Medium | 81,920                 | ~410K         |
| Large  | 163,840                | ~820K         |

## Performance Results

### ⚡ Write Performance

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| **Badger**   | **1.37s** | **2.90s**  | 7.46s |
| **Persist**  | 1.45s | 2.90s  | **5.99s** |
| BuntDB   | 1.82s | 3.71s  | 7.43s |
| Pebble   | 3.33s | 5.78s  | 11.21s |
| VoidDB   | 3.43s | 6.86s | 15.90s |
| BoltDB   | 5.21s | 9.94s | 262.16s |

### 🔎 Single Record Lookup (Random)

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| **BoltDB**   | **0.10s** | **0.14s**  | 0.28s |
| **VoidDB**   | 0.10s | 0.13s  | **0.22s** |
| Badger   | 0.12s | 0.21s  | 0.36s |
| Pebble   | 0.63s | 0.64s  | 0.46s |
| Persist  | 0.71s | 1.45s  | 3.09s |
| BuntDB   | 0.71s | 1.65s  | 3.63s |

BoltDB and VoidDB provide fast reads directly from disk without significant upfront loading. Badger uses partial loading strategies with good performance. Persist and BuntDB, designed for repeated efficient access, load data into memory initially, incurring a one-time startup cost.

### 📚 Batch Read (40K random records)

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| **Persist**  | **0.78s** | **1.65s**  | **3.48s** |
| **BuntDB**   | 0.83s | 1.67s  | 3.61s |
| Badger   | 0.89s | 1.39s  | 5.03s |
| VoidDB   | 1.75s | 3.42s  | 6.83s |
| Pebble   | 1.94s | 3.21s  | 6.07s |
| BoltDB   | 2.87s | 3.90s  | 4.42s |

For random access patterns, Persist and BuntDB show excellent performance. Badger performs well on small/medium datasets but scales less optimally with large datasets under random access. BoltDB maintains consistent performance across dataset sizes, suggesting less sensitivity to dataset growth when accessing random records.

### 💾 Storage Efficiency

| Solution | Small (~205K) | Medium (~410K) | Large (~820K) | Compression |
|----------|---------------|----------------|---------------|-------------|
| **Badger**   | **236 MB** | **474 MB**  | **943 MB** | ✅ Yes |
| Persist  | 241 MB | 483 MB | 967 MB | ❌ No |
| BuntDB   | 246 MB | 492 MB | 986 MB | ❌ No |
| Pebble   | 260 MB | 503 MB | 983 MB | ✅ Yes |
| BoltDB   | 420 MB | 826 MB | 1636 MB | ❌ No |
| VoidDB   | 1123 MB | 2245 MB | 4491 MB | ❌ No |

Badger and Pebble use built-in data compression (Snappy), contributing to their excellent storage efficiency. Persist and BuntDB show comparable storage efficiency without compression mechanisms. BoltDB uses significantly more disk space, while VoidDB requires substantially more space than all other solutions.

This is an important consideration when selecting a solution for specific use cases: with low-entropy data (like JSON with highly repetitive structures and small data variations), the compression advantage of Badger and Pebble would be more pronounced.

> [!NOTE]
> VoidDB currently does not pack small values (< 4 KiB) for speed of access and simplicity of design. This results in wasted space, especially if the dataset consists of a large number of small values.

### 📈 Memory Usage After Initial Load (single query)

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| **VoidDB**   | **0 bytes** | **0 bytes** | **0 bytes** |
| **BoltDB**   | 5.1 KB | 5.2 KB | 5.1 KB |
| Pebble   | 2.4 MB | 2.3 MB | 2.1 MB |
| Badger   | 85.7 MB | 85.8 MB | 85.9 MB |
| BuntDB   | 266.8 MB | 533.9 MB | 1.0 GB |
| Persist  | 301.8 MB | 602.8 MB | 1.2 GB |

VoidDB shows exceptionally low initial memory usage, followed by BoltDB's consistent minimal footprint. Badger shows consistent memory requirements regardless of dataset size. Persist and BuntDB use significant memory upfront to enable ultra-low-latency repeated access thereafter.

### 📈 Memory Usage After Batch Reads (40K random reads)

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| **VoidDB**   | **59.0 MB** | **59.0 MB** | **59.0 MB** |
| **BoltDB**   | 68.6 MB | 68.6 MB | 68.6 MB |
| Pebble   | 63.1 MB | 64.0 MB | 71.3 MB |
| BuntDB   | 327.2 MB | 594.2 MB | 1.1 GB |
| Persist  | 302.8 MB | 603.8 MB | 1.2 GB |
| Badger   | 374.1 MB | 394.8 MB | 1.3 GB |

With random access patterns, memory usage increases significantly for most solutions compared to sequential access. VoidDB shows remarkably consistent and efficient memory usage across all dataset sizes. Badger's memory consumption during random batch operations scales with dataset size and becomes substantial with larger datasets. Persist and BuntDB maintain their memory-intensive approach as expected from their design.

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
| **VoidDB**   | **0.88s** | **8.38s** | **0.01s** | 6.7 KB | 3446.8 MB (5255.6 MB logical) |
| **Badger**   | 1.10s | 12.27s | 0.04s | 88.2 MB | **3443.6 MB** |
| BoltDB   | 1.60s | 23.65s | 0.08s | **3.2 KB** | 3444.6 MB |
| Pebble   | 3.05s | 52.17s | 0.11s | 12.1 MB | 3463.9 MB |
| Native FS | 0.90s | 8.35s | 0.01s | 2.5 MB | 3445.2 MB |

VoidDB delivers the fastest write speeds, comparable to native filesystem operations. Badger balances good performance with optimal storage efficiency. The difference between VoidDB's logical and physical sizes indicates its use of sparse files. For single file reading, VoidDB and native filesystem operations provide the best performance.

## Conclusion

> [!NOTE]
> This benchmark focuses specifically on load characteristics, storage efficiency, and scaling properties rather than [operational performance](https://github.com/Jipok/go-persist/tree/master/benchmark).

- **Persist**: Excellent batch read performance (especially with random access), good for applications needing fast repeated access with reasonable memory availability
- **Badger**: Balanced performance across most operations with excellent storage efficiency; the most versatile solution though memory usage scales with dataset size for random access
- **BoltDB**: Minimal memory footprint and exceptional open-read-close performance, ideal for serverless or sporadic access patterns, but watch out for huge write times on large datasets
- **VoidDB**: Extremely low and consistent memory usage with excellent single-record performance, very fast file operations, though storage inefficient
- **Pebble**: Good storage efficiency but generally middle-of-pack performance
- **BuntDB**: Similar performance profile to Persist but with slightly higher storage requirements