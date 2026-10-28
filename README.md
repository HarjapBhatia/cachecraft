# CacheCraft: Distributed Cache Topology and Saturation Analysis

## 1. Abstract
CacheCraft is an empirical research and benchmarking platform designed to analyze database connection pool exhaustion under high-concurrency read operations. By orchestrating a multi-node cluster behind an Nginx load balancer, the framework evaluates the mechanics of progressive caching strategies:
1. No Cache (Unprotected baseline demonstrating pool starvation)
2. Cache-Aside (Standard read-miss-write caching demonstrating stampede penetration)
3. Local Single-Flight (In-memory request coalescing per process)
4. Distributed Lock (Cluster-wide request coalescing via Redis SET NX PX and Lua atomic release)
5. Stochastic TTL Jitter (Mitigating cache avalanches caused by bulk synchronized expirations)

This Go edition is built from scratch in Go 1.24+, leveraging lightweight goroutines (M:N cooperative scheduling), the canonical `golang.org/x/sync/singleflight` package, and `database/sql` context deadline propagation to provide exact parity with HikariCP saturation physics while slashing memory overhead by over 90%.

---

## 2. System Architecture & Topology

```text
               +----------------------------------+
               |        Load Generator (k6)       |
               +----------------------------------+
                                |
                                v
               +----------------------------------+
               |        Nginx (Port 8080)         |
               |       Round-Robin Balancer       |
               +----------------------------------+
                     /          |          \
                    /           |           \
                   v            v            v
           +------------+ +------------+ +------------+
           | Go API-1   | | Go API-2   | | Go API-3   |
           | (Port 8081)| | (Port 8082)| | (Port 8083)|
           +------------+ +------------+ +------------+
                   \            |            /
                    \           |           /
                     v          v          v
               +----------------------------------+
               |             Redis 7              |
               |  (Caching, Locking, Pipelining)  |
               +----------------------------------+
                                |
                                v
               +----------------------------------+
               |          PostgreSQL 16           |
               |     (10,000 Seeded Items)        |
               +----------------------------------+
```

### Artificial Constraints for Saturation Emulation:
* **Connection Pool Ceiling**: `database/sql` is strictly configured to a maximum of 20 connections per API instance (`db.SetMaxOpenConns(20)`), establishing 60 total global connections across the 3-node cluster.
* **Artificial Query Delay**: A `pg_sleep(CAST($1 AS double precision) / 1000.0)` is executed in a CTE within PostgreSQL, ensuring the checked-out connection is occupied for exactly the configured duration (e.g. 250ms).
* **Pool Starvation Timeout**: A `context.WithTimeout(ctx, 2*time.Second)` is applied to each query. When concurrent requests waiting for an open connection breach this 2000ms threshold, `context.DeadlineExceeded` is returned and mapped to an HTTP 503 `DATABASE_CONNECTION_TIMEOUT`.
* **Concurrency Pressure**: The Go HTTP server handles incoming requests on lightweight goroutines (~2KB initial stack), allowing hundreds of concurrent connections to queue up without OS thread starvation.

---

## 3. Caching Strategies Overview

### 3.1. No Cache (`strategy=no-cache`)
Every request bypasses the cache and issues SQL statements directly to PostgreSQL with the configured delay. Under 200 concurrent requests with a 250ms delay, the 20-connection pool is exhausted and the final requests fail with HTTP 503 after queuing for 2,000ms.

### 3.2. Cache-Aside (`strategy=cache-aside`)
Standard read-through caching. On a cold key under 600 concurrent requests across 3 nodes, multiple requests arrive before the first query writes the item into Redis. This allows several queries to penetrate PostgreSQL before Redis is populated.

### 3.3. Local Single-Flight (`strategy=local-single-flight`)
Employs Go's `golang.org/x/sync/singleflight` package to coalesce concurrent duplicate requests within the same process. When 600 requests arrive across 3 API nodes, each node elects a local leader to query PostgreSQL while the remaining requests on that node wait for the shared result. This reduces database queries to strictly 3 (1 per node).

### 3.4. Distributed Lock (`strategy=distributed-lock`)
Coordinates the entire cluster using Redis:
1. The first goroutine to arrive executes `SET lock:item:{id} {uuid} NX PX 5000`.
2. The winning leader executes the PostgreSQL query, stores the result in Redis, and releases the lock atomically via a Lua script.
3. Followers across all 3 nodes enter a bounded polling loop with randomized exponential jitter (5ms to 50ms), serving the item from Redis once populated.
4. Result: Across 600 concurrent requests over 3 nodes, strictly 1 database query is executed globally.

### 3.5. Stochastic TTL Jitter (`strategy=cache-aside-jitter`)
Avalanche mitigation. When 10,000 items are pre-populated with identical expiration timestamps via pipelined absolute expiry (`POST /debug/cache/bulk-warm`), subsequent reads apply a symmetric uniform random variation to the base TTL:
$$TTL_{effective} = TTL_{base} \times (1.0 + \text{rand}(-jitter, +jitter))$$
This desynchronizes cache misses across a time window, smoothing out the query volume to PostgreSQL.

---

## 4. Quick Start

### 4.1. Option A: GitHub Codespaces (Recommended)
This repository includes a pre-configured `.devcontainer` with Go 1.24, Docker-in-Docker, and k6 pre-installed.
1. Click **Code** -> **Codespaces** -> **Create codespace on main**.
2. Once the cloud terminal opens, launch the cluster:
   ```bash
   docker compose up --build -d
   ```
3. Verify cluster health:
   ```bash
   docker compose ps
   ```

### 4.2. Option B: Local Workstation
#### Prerequisites
* **Go 1.24+** (for local development and unit tests)
* **Docker & Docker Compose** (Docker Desktop or Linux Docker Engine)
* **k6** (for executing load saturation tests)

#### Setup Environment
```bash
cp .env.example .env
```

#### Running the Test Suite Locally
```bash
go test -v ./...
```

#### Launching the Cluster
```bash
docker compose up --build -d
```

Verify that all 6 containers are healthy:
```bash
docker compose ps
```

---

## 5. Running the Saturation Experiments

Use the automated benchmark runner (`run-test.sh` on Linux/macOS/Git Bash, or `run-test.ps1` on Windows PowerShell):

### 5.1. Baseline (Warm Cache Maximum Ceiling)
```bash
bash ./run-test.sh type=baseline strat=cache-aside delay=0 ttl=300
# Or in PowerShell:
# .\run-test.ps1 -Type baseline -Strat cache-aside -Delay 0 -TTL 300
```

### 5.2. Experiment 1: Unprotected System (No Cache Stampede)
Targeting node 1 directly (`node=1` or `-Node 1`):
```bash
bash ./run-test.sh type=stampede strat=no-cache n=200 delay=250 node=1
# Or in PowerShell:
# .\run-test.ps1 -Type stampede -Strat no-cache -N 200 -Delay 250 -Node 1
```

### 5.3. Experiment 2: Naive Caching (Cache Aside Stampede)
Targeting the 3-node cluster through Nginx (default):
```bash
curl -X POST http://localhost:8080/debug/cache/reset
bash ./run-test.sh type=stampede strat=cache-aside n=600 delay=250
# Or in PowerShell:
# .\run-test.ps1 -Type stampede -Strat cache-aside -N 600 -Delay 250
```

### 5.4. Experiment 3: JVM/Process Coalescing (Local Single Flight)
```bash
bash ./run-test.sh type=stampede strat=local-single-flight n=600 delay=250
# Or in PowerShell:
# .\run-test.ps1 -Type stampede -Strat local-single-flight -N 600 -Delay 250
```

### 5.5. Experiment 4: Cluster-Boundary Coalescing (Distributed Lock)
```bash
bash ./run-test.sh type=stampede strat=distributed-lock n=600 delay=250
# Or in PowerShell:
# .\run-test.ps1 -Type stampede -Strat distributed-lock -N 600 -Delay 250
```

### 5.6. Experiment 5: Synchronized Expiration Avalanche (No Jitter)
```bash
bash ./run-test.sh type=avalanche strat=cache-aside ttl=5 delay=5 jitter=0.0
# Or in PowerShell:
# .\run-test.ps1 -Type avalanche -Strat cache-aside -TTL 5 -Delay 5 -Jitter 0.0
```

### 5.7. Experiment 6: Stochastic Expiry Distribution (TTL Jitter)
```bash
bash ./run-test.sh type=avalanche strat=cache-aside-jitter ttl=5 delay=5 jitter=0.10
# Or in PowerShell:
# .\run-test.ps1 -Type avalanche -Strat cache-aside-jitter -TTL 5 -Delay 5 -Jitter 0.10
```

---

## 6. Empirical Benchmark Results & Architectural Analysis

The following empirical benchmark matrix documents live tests executed against the 3-node Go cluster running behind the Nginx round-robin load balancer, PostgreSQL 16 (10,000 seeded items), and Redis 7.

### 6.1. Master Benchmark Results Table

| Exp # | Experiment Scenario | Strategy (`strat`) | Concurrency (VUs) | Target Topology | DB Delay | Throughput | p50 Latency | p95 Latency | Max Latency | Cache Hit Rate | HTTP 503 Errors | DB Queries Delta (API 1 \| 2 \| 3) |
| :---: | :--- | :--- | :---: | :--- | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :--- |
| **1** | **Baseline Maximum Ceiling** | `cache-aside` | 10 (Sustained 10s) | Nginx (3 Nodes) | 0ms | 1,165.5 req/s | 7.8 ms | 15.0 ms | 38.1 ms | 99.95% | 0 | **+6** (+2 \| +2 \| +2) |
| **2A** | **Single-Node Pool Starvation** | `no-cache` | 200 (Burst) | API-1 Direct | 250ms | 94.8 req/s | 1,376.5 ms | 2,030.8 ms | 2,046.4 ms | 0.00% | 0 | **+200** (+200 \| +0 \| +0) |
| **2B** | **Cluster-Wide Pool Starvation** | `no-cache` | 600 (Burst) | Nginx (3 Nodes) | 250ms | 241.3 req/s | 1,416.4 ms | 2,199.5 ms | 2,266.2 ms | 0.00% | **118** | **+600** (+200 \| +200 \| +200) |
| **3** | **Naive Cache-Aside Stampede** | `cache-aside` | 600 (Burst) | Nginx (3 Nodes) | 250ms | 241.5 req/s | 1,650.4 ms | 2,216.3 ms | 2,322.2 ms | 0.00% | **192** | **+600** (+200 \| +200 \| +200) |
| **4** | **Local Single-Flight Coalescing** | `local-single-flight` | 600 (Burst) | Nginx (3 Nodes) | 250ms | 1,109.4 req/s | 316.0 ms | 408.8 ms | 417.1 ms | 0.00% | **0** | **+3** (+1 \| +1 \| +1) |
| **5** | **Redis Distributed Lock** | `distributed-lock` | 600 (Burst) | Nginx (3 Nodes) | 250ms | 735.2 req/s | 502.8 ms | 653.3 ms | 680.0 ms | 99.83% | **0** | **+1** (+0 \| +1 \| +0) |
| **6** | **Distributed Lock (Single Node)** | `distributed-lock` | 600 (Burst) | API-1 Direct | 250ms | 1,023.3 req/s | 331.3 ms | 391.2 ms | 405.3 ms | 99.83% | **0** | **+1** (+1 \| +0 \| +0) |
| **7** | **Zero-Delay Cache Stampede** | `cache-aside` | 600 (Burst) | Nginx (3 Nodes) | 0ms | 919.4 req/s | 297.8 ms | 438.5 ms | 462.5 ms | 98.67% | **0** | **+8** (+3 \| +3 \| +2) |
| **8** | **High-Stress 1,000 VUs Lock** | `distributed-lock` | 1,000 (Burst) | Nginx (3 Nodes) | 250ms | 831.7 req/s | 435.5 ms | 566.8 ms | 586.8 ms | 99.90% | **0** | **+1** (+0 \| +1 \| +0) |
| **9** | **Synchronized Expiry Avalanche** | `cache-aside` (Jitter=0.0) | 500 (2000 req/s) | Nginx (3 Nodes) | 5ms | 638.7 req/s | 445.1 ms | 848.5 ms | 3,419.6 ms | 33.89% | **10** | **+6,102** (+2065 \| +2046 \| +1991) |
| **10** | **Stochastic TTL Jitter Defense** | `cache-aside-jitter` (Jitter=0.10) | 500 (2000 req/s) | Nginx (3 Nodes) | 5ms | 683.5 req/s | 394.4 ms | 780.8 ms | 1,347.9 ms | 35.78% | **0** | **+6,276** (+2074 \| +2106 \| +2096) |

---

### 6.2. Deep Dive: Architectural Whys & Engineering Takeaways

#### 1. Connection Pool Exhaustion Mechanics (Experiments 2A & 2B)
* **The "What"**: Under 600 concurrent un-cached requests (`strat=no-cache`) with an artificial 250ms query delay, **118 requests timed out with HTTP 503 `DATABASE_CONNECTION_TIMEOUT`**, and max latency breached 2.26 seconds.
* **The "Why"**: Each Go API instance enforces a hard ceiling of 20 connections (`db.SetMaxOpenConns(20)`), establishing 60 total database connections cluster-wide. Under a 2,000ms context deadline (`context.WithTimeout`), each connection can complete at most $2000 / 250 = 8$ queries before the deadline expires ($20 \times 8 = 160$ queries per instance, 480 global). The remaining 120 requests queued in memory until their context deadlines were breached.

#### 2. The Fallacy of Naive Cache-Aside on Cold Keys (Experiment 3)
* **The "What"**: In standard Cache-Aside, querying a cold key under 600 concurrent requests resulted in **600 database queries** (+200 on each node) and **192 HTTP 503 failures**—identical to having no cache at all.
* **The "Why"**: Because all 600 requests arrived in the same millisecond burst before the first query could finish its 250ms database round-trip and populate Redis, all 600 requests simultaneously observed a cache miss. Having a cache provides zero protection against cold-start bursts without concurrency coalescing.

#### 3. In-Memory Single-Flight vs. Cluster Distributed Locking (Experiments 4 & 5)
* **Local Single-Flight (`golang.org/x/sync/singleflight`)**:
  - **Result**: Collapsed 600 requests into **strictly 3 database queries** (+1 on API 1, +1 on API 2, +1 on API 3).
  - **Latency & Errors**: 0 errors, max latency dropped by 82% from 2,322ms to 417ms.
  - **Limitation**: `singleflight` operates exclusively in-memory within a single Go process. It cannot coordinate across network boundaries between distinct microservice containers.
* **Redis Distributed Locking (`SET NX PX` + Lua atomic release)**:
  - **Result**: Collapsed 600 requests into **strictly 1 database query globally** (+0 on API 1, +1 on API 2, +0 on API 3).
  - **Coordination**: The winning leader executed the query and unlocked via Lua; all other 599 goroutines across all 3 nodes polled Redis with exponential jitter and served the cached response with a 99.83% cache hit rate.

#### 4. The Sub-Millisecond Database Myth (Experiment 7)
* **The "What"**: Even with a **0ms query delay** (sub-millisecond PostgreSQL response), naive Cache-Aside still allowed **8 database queries** to penetrate (+3 on API 1, +3 on API 2, +2 on API 3).
* **The "Why"**: The race window between checking Redis, executing a native SQL query, and writing the result to Redis is non-zero (1–2ms). Under concurrent arrival, parallel requests still slip through before the cache write completes. Fast databases do not eliminate stampedes.

#### 5. Cache Avalanche Mitigation via Stochastic TTL Jitter (Experiments 9 & 10)
* **The "What"**: When 10,000 keys expired at the identical millisecond (Jitter = 0.0), a sudden spike of 6,102 queries slammed PostgreSQL within seconds. Hit rate plummeted to 33.89%, max latency exploded to **3,419.59 ms** (3.42s), and 10 requests timed out with HTTP 503.
* **The "Why"**: Applying symmetric uniform random variation:
  $$TTL_{\text{effective}} = TTL_{\text{base}} \times \left(1.0 + \text{rand}(-0.10, +0.10)\right)$$
  desynchronized key expirations over a 1-second temporal band ($[4.5\text{s}, 5.5\text{s}]$).
* **The Result**:
  - **100% elimination of HTTP 503 errors** (dropped from 10 to 0).
  - **60.6% reduction in max latency** (dropped from 3,420ms to 1,348ms).
  - **33.4% reduction in p99 latency** (dropped from 1,524ms to 1,014ms).
  - Sustained higher request throughput (+45 req/s).

---

## 7. Project Layout

```text
CacheCraft/
├── .devcontainer/                # GitHub Codespaces container specification
│   └── devcontainer.json         # Automated Go 1.24, Docker-in-Docker & k6 setup
├── cmd/server/main.go            # Application composition root and graceful shutdown
├── internal/
│   ├── config/config.go          # 12-factor environment configuration
│   ├── domain/                   # Business entities (Item, CacheResponse, ApiError)
│   ├── service/                  # Ports and Strategy Coordinator (ItemService)
│   ├── repository/               # PostgreSQL database/sql adapter with atomic counter
│   ├── cache/                    # Redis client adapter with symmetric TTL jitter
│   ├── lock/                     # SingleFlight and Redis Distributed Lock adapters
│   └── transport/http/           # Chi router, HTTP handlers, and JSON error mappers
├── scripts/                      # SQL schema DDL and 10,000 deterministic seed items
├── k6-load-tests/                # k6 load testing scripts (baseline, stampede, avalanche)
├── .dockerignore                 # Docker build context exclusions
├── .env.example                  # Environment configuration template
├── .gitignore                    # Git tracking ignore rules
├── Dockerfile                    # Multi-stage Docker build (~16MB Alpine image)
├── docker-compose.yml            # 3 Go nodes, PostgreSQL, Redis, and Nginx
├── nginx.conf                    # Round-robin reverse proxy configuration
├── run-test.sh                   # Bash benchmark harness with query delta tracking
└── run-test.ps1                  # PowerShell benchmark harness with query delta tracking
```
