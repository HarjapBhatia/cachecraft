# CacheCraft: Distributed Cache Topology and Saturation Analysis (Go Edition)

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

## 6. Project Layout

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
