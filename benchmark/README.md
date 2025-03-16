# Performance Benchmark for `go-persist`

This benchmark measures the performance of `go-persist` against popular Go embedded databases (`BoltDB`, `BuntDB`) and standard concurrency-safe maps (`sync.Map`, `map` guarded by `sync.RWMutex`). The benchmarks provide insights across various usage scenarios and dataset sizes, including both structured data (Go structs) and plain strings.

---

## 📌 System Specifications

- **OS**: Void Linux (Kernel 6.12.16)
- **CPU**: Intel N100 (Quad-core, 3.4GHz)
- **RAM**: 16 GB
- **Storage**: Local SSD
- **Testing Environment**: Benchmarks ran in a clean environment with system rebooted before/after each test to ensure consistency.

---

## 🧪 Benchmark Scenarios

The benchmarks simulate typical application workloads across various scenarios, adjusting:

- **Dataset Size**: Ranging from small datasets (10,000 keys) up to large datasets (5 million keys)
- **Operation Counts**: From 10,000 up to 10 million operations
- **Concurrency**: From 10 to 200 goroutines
- **Read/Write Ratio**: Mostly read-heavy workloads (80–90% reads)

---

## 🚀 Performance Results

### ▶️ Scenario 1: Typical Application Workload
- **Dataset**: 100,000 pre-filled keys
- **Workload**: 1,000,000 operations (20% writes, 80% reads), 150 goroutines

#### Struct Operations

| Solution             | Time   | Throughput (ops/sec)  | Latency/op | File Size |
|----------------------|--------|-----------------------|------------|-----------|
| go-persist Async ⚡️  | 141ms  | **7,117,079**         | **140ns**  | 6.07MB    |
| sync.Map             | 181ms  | 5,509,706             | 181ns      | N/A       |
| map+RWMutex          | 395ms  | 2,532,314             | 394ns      | N/A       |
| go-persist Set       | 683ms  | 1,463,708             | 683ns      | 6.07MB    |
| BuntDB               | 3981ms | 251,218               | 3980ns     | 11.15MB   |
| Bolt NoSync          | 5510ms | 181,481               | 5510ns     | 24.00MB   |

#### String Operations

| Solution             | Time   | Throughput (ops/sec)  | Latency/op | File Size |
|----------------------|--------|-----------------------|------------|-----------|
| go-persist Async ⚡️  | 126ms  | **7,932,430**         | **126ns**  | 23.56MB   |
| sync.Map             | 167ms  | 5,990,615             | 166ns      | N/A       |
| map+RWMutex          | 402ms  | 2,486,622             | 402ns      | N/A       |
| go-persist Set       | 686ms  | 1,457,657             | 686ns      | 23.56MB   |
| BuntDB               | 2449ms | 408,388               | 2448ns     | 27.21MB   |
| Bolt NoSync          | 5061ms | 197,588               | 5061ns     | 24.00MB   |

---

### ▶️ Scenario 2: Small-scale Fast Operations
- **Dataset**: 10,000 keys pre-filled
- **Workload**: 10,000 operations (20% writes, 80% reads), 10 goroutines

| Solution           | Time   | Throughput (ops/sec)  | Latency/op | File Size |
|--------------------|--------|-----------------------|------------|-----------|
| go-persist Async ⚡️| 2ms    | **5,996,613**         | **166ns**  | 0.69MB    |
| map+RWMutex        | 3ms    | 3,274,997             | 305ns      | N/A       |
| go-persist Set     | 5ms    | 2,211,860             | 452ns      | 0.69MB    |
| sync.Map           | 6ms    | 1,675,863             | 596ns      | N/A       |
| BuntDB             | 29ms   | 345,067               | 2897ns     | 0.73MB    |
| Bolt NoSync        | 97ms   | 103,106               | 9698ns     | 2.00MB    |
| go-persist FSync🔒 | 1387ms | 7,211                 | 138,666ns  | 0.69MB    |
| Bolt (default)🔒   | 2448ms | 4,084                 | 244,838ns  | 2.00MB    |
| BuntDB SyncAlways🔒| 2568ms | 3,893                 | 256,820ns  | 0.73MB    |

*(🔒 fsync after each operation; maximum durability, lowest throughput)*

---

### ▶️ Scenario 3: Large-scale Intensive Operations
- **Dataset**: 5,000,000 keys pre-filled
- **Workload**: 5,000,000 operations (10% writes, 90% reads), 200 goroutines

#### Struct Operations

| Solution             | Time    | Throughput (ops/sec)  | Latency/op | File Size |
|----------------------|---------|-----------------------|------------|-----------|
| go-persist Async ⚡️  | 2006ms  | **2,492,912**         | **401ns**  | 349.14MB  |
| map+RWMutex          | 3263ms  | 1,532,530             | 652ns      | N/A       |
| go-persist Set       | 3574ms  | 1,399,096             | 714ns      | 349.14MB  |
| sync.Map             | 5063ms  | 987,524               | 1012ns     | N/A       |
| Bolt NoSync          | 25663ms | 194,832               | 5132ns     | 632.15MB  |
| BuntDB               | 83679ms | 59,752                | 16735ns    | 367.16MB  |

#### String Operations

| Solution              | Time    | Throughput (ops/sec)  | Latency/op | File Size |
|-----------------------|---------|-----------------------|------------|-----------|
| go-persist Async ⚡️   | 878ms   | **5,692,728**         | **175ns**  | 417.04MB  |
| go-persist Set        | 2916ms  | 1,714,752             | 583ns      | 417.04MB  |
| map+RWMutex           | 3324ms  | 1,504,208             | 664ns      | N/A       |
| sync.Map              | 5167ms  | 967,686               | 1033ns     | N/A       |
| BuntDB                | 20197ms | 247,555               | 4039ns     | 498.87MB  |
| Bolt NoSync           | 21823ms | 229,117               | 4364ns     | 792.20MB  |

---

## 📊 Summary

- **Highest throughput** at all test scales
- Linear scalability with predictable performance
- Small, efficient WAL-based persisted file sizes
- Type-safe, performance comparable to native `sync.Map`
