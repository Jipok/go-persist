# Load Performance Benchmark Results

This benchmark focuses on load time and scaling characteristics across different dataset sizes for three embedded Go key-value systems.

The measurements include file loading, initialization costs, and subsequent operations to evaluate how each solution handles increasingly larger datasets. Pure operational performance is covered in a [other benchmark](https://github.com/Jipok/go-persist/tree/master/benchmark).

## Dataset Details

- **Structure**: Each record (`ComplexRecord`) includes multiple string fields, nested metadata, and a fixed 1KB data payload.
- **Key size**: 9–13 bytes.
- **Average record size**: 1228 ± 4 bytes.
- **Collections**: 5 separate collections ("maps") per database.

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
| **Badger**   | **1.05s** | **2.14s**  | **4.59s** | Linear      |
| **Persist**  | 1.80s | 3.59s  | 7.67s | Linear      |
| BuntDB   | 1.85s | 4.52s  | 7.84s | Linear      |
| Pebble   | 1.85s | 3.73s  | 7.98s | Linear      |
| VoidDB   | 5.91s | 12.39s | 26.82s | Linear      |
| BoltDB   | 5.36s | 12.68s | 267.52s | Exponential |

### 🔎 Single Record Lookup ("map-3-key-40000")

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| BoltDB   | 0.00s | 0.01s  | 0.01s |
| VoidDB   | 0.03s | 0.02s  | 0.06s |
| Badger   | 0.08s | 0.09s  | 0.13s |
| Pebble   | 0.08s | 0.13s  | 0.18s |
| BuntDB   | 0.66s | 1.59s  | 3.50s |
| Persist  | 0.80s | 1.68s  | 3.47s |

BoltDB and VoidDB provide near-instant reads directly from disk without significant upfront loading. Badger and Pebble use partial loading strategies with good performance. Persist and BuntDB, designed for repeated efficient access, load data into memory initially, incurring a one-time startup cost.

### 📚 Batch Read (40K sequential records)

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| **Badger**   | **0.54s** | **0.60s**  | **1.00s** |
| Pebble   | 0.72s | 0.88s  | 1.36s |
| Persist  | 0.85s | 1.77s  | 3.58s |
| BuntDB   | 0.88s | 1.67s  | 3.79s |
| VoidDB   | 1.78s | 3.09s  | 6.07s |
| BoltDB   | 2.95s | 3.25s  | 3.82s |

Badger stands out with exceptional batch read performance, followed by Pebble. Once loaded, go-persist and BuntDB provide good performance especially. BoltDB's disk-based lookups show consistent but slower performance across all sizes.

### 💾 Storage Efficiency

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| Badger   | 32 MB | 64 MB  | 128 MB |
| Pebble   | 43 MB | 70 MB  | 126 MB |
| Persist  | 246 MB| 492 MB | 985 MB |
| BuntDB   | 249 MB| 499 MB | 1000 MB |
| BoltDB   | 420 MB| 826 MB | 1636 MB |
| VoidDB   | 1122 MB| 2245 MB | 4490 MB |

Badger and Pebble demonstrate outstanding storage efficiency, requiring significantly less disk space than other solutions. Despite go-persist's focus on speedy in-memory access, it achieves storage efficiency comparable with BuntDB and significantly better (~40%) than BoltDB.

### 📈 Memory Usage After Initial Load (single query)

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| BoltDB   | **5.2 KB** | **5.5 KB** | **5.4 KB** |
| VoidDB   | 766.2 KB | 766.5 KB | 109.7 KB |
| Pebble   | 342.8 KB | 414.2 KB | 568.1 KB |
| Badger   | 85.7 MB | 85.7 MB | 85.7 MB |
| BuntDB   | 268.4 MB | 536.9 MB | 1.0 GB |
| Persist  | 302.7 MB | 605.3 MB | 1.2 GB |

BoltDB maintains negligible RAM usage due to its disk-based approach. VoidDB and Pebble also perform well with minimal memory requirements. Badger exhibits consistent memory usage regardless of database size. Persist and BuntDB use significant memory upfront to enable ultra-low-latency repeated access thereafter.

### 📈 Memory Usage After Batch Reads (40K reads)

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| Pebble   | 916.6 KB | 416.6 KB | 1.0 MB |
| VoidDB   | 1.7 MB | 1.5 MB | 1.4 MB |
| BoltDB   | 3.1 MB | 1.9 MB | 2.4 MB |
| Badger   | 219.8 MB | 207.9 MB | 706.6 MB |
| BuntDB   | 329.1 MB | 597.5 MB | 1.1 GB |
| Persist  | 304.7 MB | 607.2 MB | 1.2 GB |

Pebble maintains the lowest memory footprint even after batch operations, followed by VoidDB and BoltDB. Badger shows significant memory usage during batch operations but still lower than the full in-memory solutions. Persist and BuntDB consume the most memory as expected from their design approach.

### 🔄 Sequential Open-Read-Close Cycles (1000 iterations)

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| **BoltDB**   | **0.05ms** | **0.05ms** | **0.06ms** |
| **VoidDB**  | 0.09ms | 0.08ms | 0.11ms |
| Pebble   | 15.28ms | 15.26ms | 17.06ms |
| BadgerDB  | 19.34ms | 19.47ms | 23.66ms |

This benchmark evaluates how quickly each database can be opened, perform a single read operation, and then closed. This pattern is particularly important for short-lived processes, serverless functions, or applications that need to access data sporadically rather than maintain long-running connections.

BoltDB demonstrates exceptional performance in this pattern, requiring only 0.05ms on average to complete the entire open-read-close cycle. VoidDB also performs admirably in this scenario. Pebble and BadgerDB, despite their other advantages, have significantly higher overhead when frequently opened and closed.

## Conclusion

> [!NOTE]
> This benchmark focuses specifically on load characteristics, storage efficiency, and scaling properties rather than [operational performance](https://github.com/Jipok/go-persist/tree/master/benchmark).

- **Badger** demonstrates exceptional performance for long-running operations across most metrics, with best-in-class write speeds, batch read performance, and excellent storage efficiency, but shows significant overhead during database initialization.

- **Pebble** offers an outstanding balance of minimal resource usage and strong performance, particularly impressive in storage efficiency and memory optimization, though with higher startup costs.

- **BoltDB** excels in memory efficiency, single record lookups, and especially shines in sequential open-read scenarios, making it ideal for short-lived processes or serverless environments. However, it struggles with write performance at larger scales.

- **Persist** provides very good write performance and strong batch read speeds once loaded, deliberately trading higher memory usage for fast subsequent operations and type-safe access to Go structs.

- **BuntDB** shows similar characteristics to Persist with well-balanced performance across different workloads.

- **VoidDB** demonstrates poor overall performance in most tests, with extremely inefficient storage usage, slow write speeds, and mediocre batch read performance. However, it performs surprisingly well in sequential open-read scenarios.
