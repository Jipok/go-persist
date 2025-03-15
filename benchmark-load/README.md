# Dataset Loading Benchmark

```
Persist write time: 3.59s
BoltDB  write time: 12.68s
BuntDB  write time: 4.52s

Persist file size:  492  MB
Persist raw load time: 0.92s
Persist one read time: 1.68s
Persist one mem usage: 605.3 MB
Persist 40k read time: 1.77s
Persist 40k mem usage: 607.2 MB

BoltDB file size:  826  MB
BoltDB raw load time: 1.53s
BoltDB one read time: 0.01s
BoltDB one mem usage: 5.5 KB
BoltDB 40k read time: 3.25s
BoltDB 40k mem usage: 1.9 MB

BuntDB file size:  499  MB
BuntDB raw load time: 0.94s
BuntDB one read time: 1.59s
BuntDB one mem usage: 536.9 MB
BuntDB 40k read time: 1.67s
BuntDB 40k mem usage: 597.5 MB
```

## Performance Trade-offs Explained

**Write Performance**: go-persist writes data significantly faster than BoltDB (3.5x) and slightly faster than BuntDB, thanks to its simplified WAL structure.

**Memory-First vs Storage-First**: The benchmarks clearly show the "load once, use many times" philosophy of go-persist:
- **Initial load cost**: go-persist pays the deserialization cost upfront (loading and parsing all structures during initialization) which results in higher initial memory usage but faster subsequent access.
- **BoltDB** follows a pure storage-first approach with near-zero memory footprint (just 5.5KB after a single read) but requires deserialization for each read operation, making it slower for repeated access patterns (3.25s vs 1.77s for 40k reads).
- **BuntDB** uses a hybrid approach similar to go-persist, explaining their similar memory profiles.

**Ready-to-use data structures**: After the initial load, go-persist provides immediate access to fully parsed Go structs without additional deserialization overhead. The small difference between one read (1.68s) and 40k reads (1.77s) demonstrates this advantage - subsequent reads are nearly free.

**Storage efficiency**: Despite keeping all data pre-parsed in memory, go-persist achieves excellent storage efficiency (492MB file size vs BoltDB's 826MB), showing the effectiveness of its simple WAL format.

### Overall, Persist trades higher memory consumption and slower individual load+read times for the benefits of immediate, type-safe in-memory access and faster write throughput.
