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
| **Persist**  | **1.80s** | **3.59s**  | **7.67s** | Linear      |
| BoltDB   | 6.33s | 12.68s | 267.52s | Exponential |
| BuntDB   | 1.87s | 4.52s  | 7.84s | Linear      |

go-persist consistently achieves the fastest write performance across all dataset sizes, demonstrating linear scalability. In contrast, BoltDB experiences exponential performance degradation as dataset size increases.

### 🔎 Single Record Lookup ("map-3-key-40000")

| Solution | Small | Medium | Large | Notes                        |
|----------|-------|--------|-------|------------------------------|
| Persist  | 0.80s | 1.68s  | 3.47s | Includes initial load time   |
| BoltDB   | 0.00s | 0.01s  | 0.01s | Direct disk access (no load) |
| BuntDB   | 0.66s | 1.59s  | 3.50s | Includes initial load time   |

BoltDB provides instant reads directly from disk without any upfront loading. Persist and BuntDB, designed for repeated efficient access, load data into memory initially, incurring a one-time startup cost.

### 📚 Batch Read (40K sequential records)

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| Persist  | **0.85s** | 1.77s  | **3.58s** |
| BoltDB   | 2.95s | 3.25s  | 3.82s |
| BuntDB   | 0.88s | **1.67s**  | 3.79s |

go-persist stands out with excellent speed in batch reads across dataset sizes. Once loaded, go-persist's data structures provide near-instant repeated access, clearly outperforming BoltDB's disk-based lookups.

### 💾 Storage Efficiency

| Solution | Small | Medium | Large |
|----------|-------|--------|-------|
| Persist  | 246 MB| 492 MB | 985 MB |
| BoltDB   | 420 MB| 826 MB |1636 MB |
| BuntDB   | 249 MB| 499 MB |1000 MB |

Despite go-persist’s focus on speedy in-memory access, it achieves storage efficiency comparable with BuntDB and significantly better (~40%) than BoltDB.

### 📈 Memory Usage After Initial Load (single query)

| Solution | Small | Medium | Large |
|----------|---------------|----------------|---------------|
| Persist  | 302.7 MB      | 605.3 MB       | 1.2 GB        |
| BoltDB   | **5.2 KB**    | **5.5 KB**     | **5.4 KB**    |
| BuntDB   | 268.4 MB      | 536.9 MB       | 1.0 GB        |

BoltDB maintains negligible RAM usage due to its disk-based approach. Persist (and similarly BuntDB) use significant memory upfront to enable ultra-low-latency repeated access thereafter.

## Choosing the Right Solution

### 🏅 go-persist
- **Best-in-class write performance** at all scales tested
- Exceptional repeated access performance after initial load
- Native Go struct support eliminates repeated parsing overhead
- Strong storage efficiency despite in-memory design
- Predictable linear scalability for stable performance characteristics

### BoltDB
- Minimal memory usage—ideal when RAM is limited
- Instant direct-from-disk read performance per individual query
- Best for applications that involve infrequent lookups or datasets larger than available memory

### BuntDB
- Balanced hybrid design (memory/disk)
- Performs well under varied workloads
- Supports transactions without excessive overhead  

### Overall, Persist trades higher memory consumption and slower individual load+read times for the benefits of immediate, type-safe in-memory access and faster write throughput.