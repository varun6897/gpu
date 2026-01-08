## GPU Telemetry Pipeline with Custom Message Queue

This repository implements an elastic, Kubernetes‑deployable GPU telemetry pipeline with a **custom in‑memory message queue**, scalable **streamers** and **collectors**, and an **HTTP API Gateway** with auto‑generated OpenAPI.

The design follows the requirements in `GPU Telemetry Pipeline Message Queue.pdf`.

If you are **new to Go, Docker, Kubernetes, or databases**, this README is written for you. You should be able to:

- Set everything up on a local kind cluster.
- See real GPU telemetry flowing through the system.
- Understand *why* each design choice was made.

---

## Table of Contents

- **Quick Start (for complete beginners)**
  - [Quick Start: One‑command setup](#quick-start-one-command-setup)
  - [What you should see](#what-you-should-see)
- **Step‑by‑Step Setup (explained)**
  - [1. Install prerequisites](#1-install-prerequisites)
  - [2. Clone this repository](#2-clone-this-repository)
  - [3. Run tests and coverage (optional but recommended)](#3-run-tests-and-coverage-optional-but-recommended)
  - [4. Create a kind cluster](#4-create-a-kind-cluster)
  - [5. Build Docker images](#5-build-docker-images)
  - [6. Load images into kind](#6-load-images-into-kind)
  - [7. Install the Helm chart](#7-install-the-helm-chart)
  - [8. Explore the API](#8-explore-the-api)
- **Architecture and Design**
  - [High‑Level Architecture](#high-level-architecture)
  - [Architecture Diagram](#architecture-diagram)
  - [Design Decisions and Rationale](#design-decisions-and-rationale)
  - [Message Queue Design](#message-queue-design)
  - [Components and How They Work](#components-and-how-they-work)
- **Build, Packaging, and Helm Workflow**
  - [Build and Packaging Instructions](#build-and-packaging-instructions)
  - [Helm Chart and Installation Workflow](#helm-chart-and-installation-workflow-step-by-step-for-beginners)
  - [Sample User Workflow](#sample-user-workflow-end-to-end-as-a-beginner)
- [How AI Assistance Was Used](#how-ai-assistance-was-used)

---

## Quick Start (one‑command setup)

If you just want to **see the system running** as fast as possible and you are on a machine that already has **Docker**, **kind**, **kubectl**, and **Helm** installed, you can use the `Makefile` helpers.

From the repo root:

```bash
cd /Users/varunv/go/src/varunv/gpu

# This will:
# 1) Create a kind cluster called "gpu" (if it does not exist),
# 2) Build Docker images for all services,
# 3) Load them into the kind cluster node,
# 4) Install/upgrade the Helm chart and show pod status.
make quickstart-deploy
```

Then check the pods:

```bash
kubectl get pods
```

You should see:

- `telemetry-postgres` – TimescaleDB (Postgres with time‑series extensions)
- `telemetry-mq` – message queue broker
- `telemetry-streamer` – CSV streamer
- `telemetry-collector` – collector
- `telemetry-api` – HTTP API

All should reach `Running` status.

To clean everything up:

```bash
make quickstart-clean
```

This will uninstall the Helm release and delete the `gpu` kind cluster.

If you prefer to do everything manually (to understand each step), keep reading.

---

## Getting Started (Step‑by‑Step)

If you are a complete beginner, follow these steps in order.

### 1. Install prerequisites

You need these tools installed:

- **Docker** – container runtime (runs your containers locally).
- **kind** – “Kubernetes in Docker” (a local Kubernetes cluster inside Docker).
- **kubectl** – Kubernetes CLI (how you talk to the cluster).
- **helm** – Kubernetes package manager (how we install this app).
- **Go 1.23+** – only if you want to run tests or build binaries yourself.

Install in this order: **Docker → kind → kubectl → helm → Go**.

Why this order?

- Docker must be working **before** kind can create a cluster.
- kubectl and helm need a cluster to talk to, which kind will provide.
- Go is only needed for running tests or local binaries, not for running the cluster.

### 2. Clone this repository

You only need to do this once:

```bash
mkdir -p ~/go/src/varunv
cd ~/go/src/varunv
git clone <this-repo-url> gpu
cd gpu
```

From now on, when this README says “repo root”, it means this `gpu` directory.

### 3. Run tests and coverage (optional but recommended)

This step is for people who want to check the code quality before running the system.

Using raw `go` commands:

```bash
go test ./api ./collector ./mq ./streamer -coverprofile=coverage.out
go tool cover -func=coverage.out
```

Or using the `Makefile` helpers:

```bash
make test           # runs go test ./...
make coverage       # runs coverage on core packages and prints summary
```

### 4. Create a kind cluster

Create and switch to a local Kubernetes cluster named `gpu`:

```bash
kind create cluster --name gpu
kubectl config use-context kind-gpu
kubectl get nodes
```

You should see at least one node (the control plane) in `Ready` or `NotReady` (if it’s still starting) state.

If you used `make quickstart-deploy` earlier, this step was already done for you by the `kind-up` target.

### 5. Build Docker images

From the repo root:

```bash
docker build -f Dockerfile.mqbroker  -t gpu-telemetry-mqbroker:latest .
docker build -f Dockerfile.streamer  -t gpu-telemetry-streamer:latest .
docker build -f Dockerfile.collector -t gpu-telemetry-collector:latest .
docker build -f Dockerfile.api       -t gpu-telemetry-api:latest .
```

Or using the `Makefile`:

```bash
make build-images
```

### 6. Load images into the kind cluster

kind runs Kubernetes **inside Docker containers**, so your images need to be copied into that environment:

```bash
kind load docker-image gpu-telemetry-mqbroker:latest --name gpu
kind load docker-image gpu-telemetry-streamer:latest  --name gpu
kind load docker-image gpu-telemetry-collector:latest --name gpu
kind load docker-image gpu-telemetry-api:latest       --name gpu
```

Or using the `Makefile`:

```bash
make load-images
```

### 7. Install the Helm chart

Now deploy all services (Postgres + MQ + streamer + collector + API) into the cluster:

```bash
helm upgrade --install telemetry-pipeline ./helm/telemetry-pipeline
kubectl get pods
```

Helm:

- Creates Deployments and Services for all components.
- Sets environment variables correctly (Postgres DSN, MQ URLs, etc.).
- Initializes the database schema (including TimescaleDB extension and hypertables).

Wait until you see:

- `telemetry-postgres` – database (TimescaleDB)
- `telemetry-mq` – message queue broker
- `telemetry-streamer` – CSV streamer
- `telemetry-collector` – collector
- `telemetry-api` – HTTP API

All in `Running` status.

If any pod is in `CrashLoopBackOff`, look at its logs:

```bash
kubectl logs deploy/telemetry-postgres
kubectl logs deploy/telemetry-streamer
kubectl logs deploy/telemetry-collector
kubectl logs deploy/telemetry-api
```

### 8. Explore the API

Expose the API service to your laptop:

```bash
kubectl port-forward svc/telemetry-api 8080:80
```

Then in your browser:

- Swagger UI: `http://localhost:8080/swagger`
- OpenAPI JSON: `http://localhost:8080/openapi.json`

In Swagger:

1. Call `GET /api/v1/gpus` to see which GPU **UUIDs** have telemetry.
2. Copy a `uuid` from the response.
3. Call `GET /api/v1/gpus/{id}/telemetry` with that UUID as `{id}` to see metrics sorted from newest to oldest.

---

## High‑Level Architecture

If you are new to Go, Docker, Kubernetes, or kind, read this section slowly; it explains **what each piece is and why it exists**.

At a high level, the system looks like this:

- **DCGM CSV telemetry** (`dcgm_metrics_20250718_134233.csv`) is the raw source of GPU metrics. Think of it as a big log file of GPU stats.
- **Telemetry Streamer service** (`cmd/streamer`):
  - All streamer pods share a **Postgres‑backed work index** (`stream_progress` table).
  - Each pod repeatedly reserves the **next CSV row index** and publishes a telemetry message to the MQ.
- **Custom MQ Broker service** (`cmd/mqbroker`):
  - In‑memory, bounded FIFO queue (`mq.InMemoryQueue`).
  - Exposed over HTTP (`/publish`, `/consume`) via `HTTPQueue` client.
  - Streamers are **producers**; collectors are **consumers**.
- **Telemetry Collector service** (`cmd/collector`):
  - Multiple pods compete for messages from the shared MQ.
  - Parse telemetry JSON and **persist** into Postgres.
- **Postgres** (`telemetry` table):
  - Durable store for all telemetry records.
  - Also stores `stream_progress` global cursor for streamers.
- **HTTP API Gateway** (`cmd/api`):
  - Exposes telemetry over REST:
    - `GET /api/v1/gpus`
    - `GET /api/v1/gpus/{id}/telemetry?start_time=&end_time=`
  - Auto‑generates OpenAPI 3.0 spec at `GET /openapi.json`.
  - Serves a built‑in Swagger UI at `GET /swagger`.
- **Helm chart** (`helm/telemetry-pipeline`):
  - Deploys all components to Kubernetes (tested with kind).
  - Allows independent scaling of **streamer**, **collector**, and **mq-broker**.

### Architecture Diagram

The diagram below shows the main data flow from the CSV file through the system to the user:

```mermaid
flowchart LR
    subgraph LocalFiles
        CSV[DCGM CSV<br/>dcgm_metrics_20250718_134233.csv]
    end

    subgraph KubernetesCluster["kind gpu cluster"]
        subgraph DB["TimescaleDB / Postgres"]
            PG[(telemetry table<br/>+ hypertable)]
            SP[(stream_progress table)]
        end

        subgraph MQBroker["MQ Broker<br/>cmd/mqbroker"]
            MQQueue[InMemoryQueue<br/>mq.InMemoryQueue]
        end

        subgraph Streamers["Streamers<br/>cmd/streamer"]
            S1[streamer pod 1]
            S2[streamer pod 2]
        end

        subgraph Collectors["Collectors<br/>cmd/collector"]
            C1[collector pod 1]
            C2[collector pod 2]
        end

        subgraph APIService["API Gateway<br/>cmd/api"]
            API[/HTTP API<br/>/api/v1/.../]
        end
    end

    User[You in browser<br/>Swagger UI] -->|HTTP/JSON| API
    API -->|SQL queries| PG

    CSV -->|read rows| S1
    CSV -->|read rows| S2

    S1 -->|reserve next_row via SQL| SP
    S2 -->|reserve next_row via SQL| SP

    S1 -->|publish telemetry messages (HTTP)| MQQueue
    S2 -->|publish telemetry messages (HTTP)| MQQueue

    MQQueue -->|consume messages (HTTP)| C1
    MQQueue -->|consume messages (HTTP)| C2

    C1 -->|INSERT telemetry rows| PG
    C2 -->|INSERT telemetry rows| PG
```

Read this diagram as a **story**:

1. The **streamer pods** read rows from the local DCGM CSV.
2. Each streamer asks the database (`stream_progress` table) “what row should I process next?” so they **never duplicate work**.
3. Each streamer turns that CSV row into a telemetry JSON message and sends it to the **MQ broker**.
4. The **collector pods** pull messages from the MQ broker and write them into the **TimescaleDB telemetry table**.
5. The **API** reads from the telemetry table and exposes clean HTTP endpoints for you.
6. You use **Swagger UI** in your browser to explore and query that telemetry.

### Design Decisions and Rationale

This section explains **why** the architecture looks the way it does.

- **Custom in‑memory message queue instead of Kafka/RabbitMQ**:
  - Requirement was to practice/illustrate **queue design** in Go.
  - An in‑memory queue plus a tiny HTTP broker is enough for a single‑node kind cluster and keeps the code easy to understand.
  - At‑most‑once delivery is acceptable for telemetry demo workloads.
- **Separate services (streamer, collector, mq-broker, api)** instead of one monolith:
  - Lets you scale producers and consumers **independently** (m×n scaling).
  - Mirrors real‑world microservice patterns without unnecessary complexity.
- **Postgres as both datastore and work coordinator**:
  - We already need Postgres to store telemetry.
  - Using a simple `stream_progress` table gives a **single source of truth** for “next CSV row to process” shared by all streamer pods.
  - This is much simpler than trying to coordinate CSV offsets by hand across pods.
- **UUID‑centric API**:
  - GPUs in telemetry have both a `gpu_id` (small integer‑like string) and a `uuid` (stable global identifier).
  - The API surfaces **UUIDs** as the primary identifier to avoid confusion if `gpu_id` values are reused or differ across hosts.
  - `GET /api/v1/gpus` lists distinct UUIDs; `GET /api/v1/gpus/{id}/telemetry` treats `{id}` as a UUID.
- **Timestamps taken at processing time**:
  - Rather than trusting CSV timestamps, the streamer records **when** it processed each row.
  - This makes it easier to reason about data freshness in a live pipeline.

### Message Queue Design

- **Interface**: `mq.Queue` (`mq/queue.go`)
  - `Publish(ctx, msg)` – enqueue; blocks when full; respects context.
  - `Consume(ctx)` – dequeue; blocks when empty; respects context.
  - `Close()` – marks queue closed; further publishes fail with `ErrClosed`.
- **In‑memory implementation**: `mq.InMemoryQueue`
  - Bounded, FIFO, concurrent safe (`sync.Mutex` + `sync.Cond`).
  - Multiple producers and consumers; at‑most‑once delivery within a process.
- **Networked broker**:
  - `cmd/mqbroker` wraps `InMemoryQueue` and exposes:
    - `POST /publish` – accepts JSON message (`id`, `key`, `payload`, `metadata`).
    - `POST /consume` – blocks until a message is ready and returns JSON.
  - `mq.HTTPQueue` implements `mq.Queue` over HTTP to talk to the broker.

This design keeps services decoupled while preserving simple FIFO semantics and enabling scaling of producers/consumers across pods.

---

## Components and How They Work

### 1. Streamer Service (`cmd/streamer`)

**Responsibility**: Read the DCGM CSV in a loop, turn each row into a `telemetry.Record`, and publish it to the MQ. Multiple streamers share one **global work index** so they never duplicate rows.

- **Shared Work Coordination**:
  - Uses Postgres table `stream_progress`:

    ```sql
    CREATE TABLE IF NOT EXISTS stream_progress (
      stream_id TEXT PRIMARY KEY,
      next_row  BIGINT NOT NULL
    );
    ```

  - On startup, each streamer pod:
    - Ensures the table exists and inserts `(stream_id, 0)` if missing.
  - To reserve the next work item:

    ```sql
    INSERT INTO stream_progress(stream_id, next_row)
    VALUES ($1, 1)
    ON CONFLICT (stream_id)
    DO UPDATE SET next_row = stream_progress.next_row + 1
    RETURNING next_row - 1;
    ```

    This returns a unique, monotonically increasing `row_index` across all pods.

- **CSV Handling and Looping**:
  - The streamer loads the CSV into memory once, skipping the header row.
  - For each reserved index `idx`, it uses `rows[idx % len(rows)]` so the CSV acts as a **looping telemetry source**.
  - `Timestamp` in the `telemetry.Record` is set to **current processing time**, per the spec.

- **Publishing to MQ**:
  - Uses `mq.NewHTTPQueue(MQ_BASE_URL)` to publish JSON‑encoded `telemetry.Record` messages to `telemetry-mq`.
  - Metadata includes `metric_name`, `hostname`, and `uuid`.

**Design decisions (streamer)**:

- Keep streamer **stateless** about progress (no local offsets); all progress lives in Postgres (`stream_progress`), which makes scaling and restarts safe.
- Use processing time for `timestamp` to reflect when the row was actually streamed, not just when it was originally recorded.
- Load the CSV once into memory to keep the implementation simple and efficient for this demo workload.

**Scaling**:

- To scale streamers to **N pods**, simply scale the deployment:

  ```bash
  kubectl scale deploy/telemetry-streamer --replicas=N
  ```

- All pods:
  - Share the same `POSTGRES_DSN` and `STREAM_ID`.
  - Share the same `stream_progress` row.
  - Cooperatively process unique `row_index` values (no duplication).

### 2. Collector Service (`cmd/collector`)

**Responsibility**: Consume telemetry messages from the MQ and persist them to Postgres.

- **Queue consumption**:
  - Uses `mq.NewHTTPQueue(MQ_BASE_URL)` as a `mq.Queue` client.
  - `collector.Run` starts `Workers` goroutines per pod:
    - Each worker `Consume`s from the MQ.
    - JSON‑decodes into `telemetry.Record`.
    - Calls `Store.Save(ctx, rec)`.

- **Persistence**:
  - Primary implementation: `collector.PostgresStore` (`collector/postgres_store.go`).
  - Table schema (created via Helm init script):

    ```sql
    CREATE TABLE IF NOT EXISTS telemetry (
      id          BIGSERIAL PRIMARY KEY,
      timestamp   TIMESTAMPTZ NOT NULL,
      metric_name TEXT        NOT NULL,
      gpu_id      TEXT        NOT NULL,
      device      TEXT        NOT NULL,
      uuid        TEXT        NOT NULL,
      model_name  TEXT        NOT NULL,
      hostname    TEXT        NOT NULL,
      container   TEXT        NOT NULL,
      pod         TEXT        NOT NULL,
      namespace   TEXT        NOT NULL,
      value       TEXT        NOT NULL,
      labels_raw  TEXT        NOT NULL
    );
    ```

**Design decisions (collector)**:

- Use a **worker pool** per pod so you can scale within a single container (via `collector.workers`) without changing deployment topology.
- Use Postgres as the durable store so the API and other consumers can reuse the same data.
- Integrate with at‑least‑once delivery by **acking messages only after** a successful `Save`, meaning unacked messages can safely be redelivered.

**Scaling**:

- Vertical within a pod: increase `collector.workers` (Helm value → `COLLECTOR_WORKERS`).
- Horizontal across pods:

  ```bash
  kubectl scale deploy/telemetry-collector --replicas=M
  ```

- All collector pods compete for messages from `telemetry-mq`, giving true **M‑consumer** scaling.

### 3. MQ Broker Service (`cmd/mqbroker`)

**Responsibility**: Provide a shared, in‑memory message queue accessible over HTTP.

- Uses `mq.InMemoryQueue` internally with configurable `QUEUE_CAPACITY`.
- Endpoints:
  - `GET /healthz` → basic health check.
  - `POST /publish` → wrap `queue.Publish`.
  - `POST /consume` → wrap `queue.Consume` (blocking until a message is available).
  - `POST /ack` → mark a consumed message (by ID) as successfully processed.
- Because it is a single deployment (usually 1 replica), it centralizes message ordering and ownership.

**Design decisions (MQ broker)**:

- Use a **simple in‑memory queue** to keep the system easy to understand and operate in a local kind cluster.
- Expose a small HTTP API so any service (or language) can publish/consume/ack without linking Go code.
- Provide **at‑least‑once semantics** by tracking in‑flight messages with a visibility timeout and re‑queueing any that are not acked in time.

### 4. API Gateway (`cmd/api`, `api/server.go`)

**Responsibility**: Expose telemetry over HTTP, with auto‑generated OpenAPI spec and Swagger UI.

#### Endpoints

- **Health**:

  - `GET /healthz` → `200 OK` if API process is up.

- **List GPUs (by UUID)**:

  - `GET /api/v1/gpus`
  - Returns an array of objects, each describing one GPU that has telemetry:
    - `uuid` – the stable GPU UUID (this is what you use in the other endpoint).
    - `device` – device name from telemetry (for example `nvidia0`).
    - `modelName` – GPU model string.

- **Telemetry by GPU UUID**:

  - `GET /api/v1/gpus/{id}/telemetry`
  - Here `{id}` is the **GPU UUID** (for example `GPU-0aba4c65-be7d-8418-977a-c950c14b989a`).
  - Optional query params:
    - `start_time` (inclusive, RFC3339)
    - `end_time` (inclusive, RFC3339)
  - Returns an array of `telemetry.Record` JSON objects:
    - Only for that **UUID**.
    - Ordered by `timestamp` **descending** (latest metrics first).

#### OpenAPI and Swagger

- `BuildOpenAPISpec()` constructs a minimal **OpenAPI 3.0** document in code, describing:
  - `/api/v1/gpus`
  - `/api/v1/gpus/{id}/telemetry`
  - `TelemetryRecord` schema.
- `GET /openapi.json` returns this spec.
- `GET /swagger` serves a small HTML page embedding **Swagger UI** (via CDN) configured to load `/openapi.json`.

This gives you a self‑contained API doc and testing interface.

---

## Build and Packaging Instructions

### Prerequisites (what you need installed)

- **Go 1.23+** (only needed if you want to run tests or build binaries locally).
- **Docker** (used by kind and to build images).
- **kind** (Kubernetes in Docker) – this lets you run a K8s cluster on your laptop.
- **kubectl** – CLI to talk to Kubernetes.
- **helm** – package manager for Kubernetes (used to install this app).

If you are a complete beginner, install these in this order: Docker → kind → kubectl → helm → Go.

### Go Build / Test (optional but recommended)

From repo root (after cloning):

```bash
cd /Users/varunv/go/src/varunv/gpu

# Run all unit tests
go test ./...

# Or just the core services with coverage
go test ./api ./collector ./mq ./streamer -coverprofile=coverage.out
go tool cover -func=coverage.out
```

### Docker Images

The repo includes Dockerfiles for each service:

- `Dockerfile.mqbroker` → `gpu-telemetry-mqbroker`
- `Dockerfile.streamer` → `gpu-telemetry-streamer`
- `Dockerfile.collector` → `gpu-telemetry-collector`
- `Dockerfile.api` → `gpu-telemetry-api`
- (Legacy) `Dockerfile.pipeline` for a combined process (not used in the current Helm setup).

Build all images:

```bash
cd /Users/varunv/go/src/varunv/gpu

docker build -f Dockerfile.mqbroker  -t gpu-telemetry-mqbroker:latest .
docker build -f Dockerfile.streamer  -t gpu-telemetry-streamer:latest .
docker build -f Dockerfile.collector -t gpu-telemetry-collector:latest .
docker build -f Dockerfile.api       -t gpu-telemetry-api:latest .
```

For use with kind you must **load** the local images into the cluster node:

```bash
kind load docker-image gpu-telemetry-mqbroker:latest --name gpu
kind load docker-image gpu-telemetry-streamer:latest  --name gpu
kind load docker-image gpu-telemetry-collector:latest --name gpu
kind load docker-image gpu-telemetry-api:latest       --name gpu
```

### OpenAPI Generation (optional)

The API already serves `/openapi.json` dynamically. If you want a **static file** copy:

```bash
# Uses cmd/api-openapi to print the spec to stdout
go run ./cmd/api-openapi > api/openapi.json
```

---

## Helm Chart and Installation Workflow (step‑by‑step for beginners)

### Chart Layout (what Helm will create)

- `helm/telemetry-pipeline/Chart.yaml` – chart metadata.
- `helm/telemetry-pipeline/values.yaml` – configurable values:
  - `mqBroker` – image, replicas, queueCapacity.
  - `streamer` – image, replicas, sleepMillis.
  - `collector` – image, replicas, workers.
  - `api` – image, replicas.
  - `postgres` – image, credentials, DB name, port.
- `helm/telemetry-pipeline/templates/`:
  - `postgres-configmap.yaml` – `init.sql` to create `telemetry` and `stream_progress` tables.
  - `postgres-deployment.yaml`, `postgres-service.yaml`.
  - `mqbroker-deployment.yaml`, `mqbroker-service.yaml`.
  - `streamer-deployment.yaml`.
  - `collector-deployment.yaml`.
  - `api-deployment.yaml`, `api-service.yaml`.

### 1. Create a kind Cluster

Run this **once** (if you don’t already have a `gpu` cluster):

```bash
kind create cluster --name gpu
kubectl config use-context kind-gpu
```

You can verify:

```bash
kubectl get nodes
```

### 2. Build and Load Images into kind

From the repo root:

```bash
cd /Users/varunv/go/src/varunv/gpu

docker build -f Dockerfile.mqbroker  -t gpu-telemetry-mqbroker:latest .
docker build -f Dockerfile.streamer  -t gpu-telemetry-streamer:latest .
docker build -f Dockerfile.collector -t gpu-telemetry-collector:latest .
docker build -f Dockerfile.api       -t gpu-telemetry-api:latest .

kind load docker-image gpu-telemetry-mqbroker:latest --name gpu
kind load docker-image gpu-telemetry-streamer:latest  --name gpu
kind load docker-image gpu-telemetry-collector:latest --name gpu
kind load docker-image gpu-telemetry-api:latest       --name gpu
```

This makes the images available **inside** the kind cluster.

### 3. Install the Helm Chart

From the repo root:

```bash
cd /Users/varunv/go/src/varunv/gpu

helm upgrade --install telemetry-pipeline ./helm/telemetry-pipeline
```

Watch pods come up:

```bash
kubectl get pods
```

You should see pods like:

- `telemetry-postgres`
- `telemetry-mq`
- `telemetry-streamer`
- `telemetry-collector`
- `telemetry-api`

Wait until all are in `Running` state.

### 4. Scaling Streamers and Collectors

- Scale streamers (more producers of telemetry messages):

```bash
kubectl scale deploy/telemetry-streamer --replicas=4
```

- Scale collectors (more consumers that write to Postgres):

```bash
kubectl scale deploy/telemetry-collector --replicas=4
```

No config change is needed: the shared `stream_progress` in Postgres ensures streamers don’t duplicate rows, and the MQ broker ensures collectors share work.

---

## Sample User Workflow (end‑to‑end, as a beginner)

1. **Bootstrap the cluster**
   - Create kind cluster (if not already created).
   - Build & load images into kind.
   - Install or upgrade the Helm release:

     ```bash
     helm upgrade --install telemetry-pipeline ./helm/telemetry-pipeline
     ```

2. **Verify telemetry ingestion**

   - Check pods:

     ```bash
     kubectl get pods
     ```

   - Confirm `telemetry-streamer`, `telemetry-collector`, `telemetry-mq`, and `telemetry-postgres` are `Running`.

3. **Explore the API via Swagger**

   - Port‑forward the API service from the cluster to your laptop:

     ```bash
     kubectl port-forward svc/telemetry-api 8080:80
     ```

   - Open in browser:
     - Swagger UI: `http://localhost:8080/swagger`
     - OpenAPI JSON: `http://localhost:8080/openapi.json`

   - In Swagger UI, try:
     - `GET /api/v1/gpus` to see which GPU **UUIDs** have telemetry.
     - Copy one of the `uuid` values.
     - `GET /api/v1/gpus/{id}/telemetry` using that UUID as `{id}`. You should see telemetry records ordered from newest to oldest.

4. **Scale for load**

   - Increase producers/consumers:

     ```bash
     kubectl scale deploy/telemetry-streamer --replicas=4
     kubectl scale deploy/telemetry-collector --replicas=4
     ```

   - Streamers will cooperatively read from the CSV via the shared Postgres cursor and push into MQ; collectors will compete for those messages and persist them.

5. **Inspect data directly in Postgres (optional, for debugging or curiosity)**

   - Port‑forward Postgres:

     ```bash
     kubectl port-forward svc/telemetry-postgres 5432:5432
     ```

   - Connect with `psql`:

     ```bash
     psql "postgres://gpu:gpu-password@localhost:5432/gpu?sslmode=disable"
     SELECT COUNT(*) FROM telemetry;
     SELECT DISTINCT uuid FROM telemetry;
     ```

---

## How AI Assistance Was Used

This project was developed with extensive AI assistance to accelerate design, implementation, and testing:

1. **Project bootstrapping**
   - AI suggested the initial high‑level architecture:
     - Custom in‑memory MQ (`mq.Queue` + `InMemoryQueue`).
     - Separate streamer, collector, and API services.
     - Use of Helm + kind for Kubernetes deployment.
   - AI helped choose idiomatic Go patterns (interfaces, packages, error handling).

2. **Message queue and broker design**
   - AI iteratively refined the `mq.Queue` interface and `InMemoryQueue` implementation:
     - Bounded capacity.
     - Concurrency safety.
     - Context‑aware blocking semantics.
   - AI proposed the HTTP broker (`cmd/mqbroker`) and `HTTPQueue` client, enabling decoupled services and m×n scaling of producers/consumers.

3. **Streamer and collector services**
   - AI helped design the initial streamer and collector flows:
     - Streamer: CSV → `telemetry.Record` → MQ.
     - Collector: MQ → `telemetry.Record` → Postgres.
   - After discussion about scaling semantics, AI designed and implemented the **Postgres‑backed `stream_progress`** table and reservation query so multiple streamer pods can safely share a single “next work item” source.
   - AI also suggested sharding by `gpu_id` initially; we later replaced this with the shared index for simpler m×n semantics.

4. **API Gateway and OpenAPI**
   - AI generated the initial HTTP handlers and route wiring in `api/server.go`.
   - AI produced the in‑code OpenAPI 3.0 spec builder (`BuildOpenAPISpec`) and the helper binary `cmd/api-openapi` to emit `openapi.json`.
   - AI added a minimal `/swagger` HTML handler that embeds Swagger UI via CDN, wired to `/openapi.json`.

5. **Helm charts and deployment**
   - AI created the Helm chart for deploying all services:
     - Postgres + init SQL ConfigMap.
     - MQ broker, streamer, collector, API.
   - AI updated values and templates as the architecture evolved (e.g., deprecating the combined pipeline deployment, wiring `POSTGRES_DSN` and `MQ_BASE_URL`, and separating services for independent scaling).

6. **Testing and coverage**
   - AI wrote unit tests across packages (`mq`, `streamer`, `collector`, `api`), including error‑path tests to push coverage above **80%**:
     - `InMemoryQueue` concurrency and context cancellation.
     - HTTPQueue happy path and error status handling.
     - Collector success and various error propagation paths.
     - API handlers’ success, validation failures, and method errors.
   - AI iteratively inspected coverage reports (`go tool cover -func`) and added tests for under‑covered branches.

7. **Iterative debugging and polish**
   - AI helped debug:
     - Initial Postgres init issues (duplicate `CREATE DATABASE`).
     - Context timeout in streamer main loop.
     - Helm upgrades not picking up new images (fixed via explicit `rollout restart` and `:latest` image use).
   - AI refined logs, error messages, and configuration defaults for better diagnostics and usability.

Overall, AI was used as a **pair programmer and systems designer**:

- The human provided architectural direction, constraints, and integration checks (e.g., kind/Helm runs, manual testing in Swagger).
- AI rapidly produced code, tests, Helm manifests, and documentation, and iteratively corrected issues based on compiler errors, linter feedback, and runtime logs.


