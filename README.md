## GPU Telemetry Pipeline with Custom Message Queue

This repository implements an elastic, Kubernetes‑deployable GPU telemetry pipeline with a **custom in‑memory message queue**, scalable **streamers** and **collectors**, and an **HTTP API Gateway** with auto‑generated OpenAPI.

The design follows the requirements in `GPU Telemetry Pipeline Message Queue.pdf`.

---

## High‑Level Architecture

At a high level, the system looks like this:

- **DCGM CSV telemetry** (`dcgm_metrics_20250718_134233.csv`) is the raw source of GPU metrics.
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
- Because it is a single deployment (usually 1 replica), it centralizes message ordering and ownership.

### 4. API Gateway (`cmd/api`, `api/server.go`)

**Responsibility**: Expose telemetry over HTTP, with auto‑generated OpenAPI spec and Swagger UI.

#### Endpoints

- **Health**:

  - `GET /healthz` → `200 OK` if API process is up.

- **List GPUs**:

  - `GET /api/v1/gpus`
  - Returns an array of `gpu_id` strings for which telemetry exists.

- **Telemetry by GPU**:

  - `GET /api/v1/gpus/{id}/telemetry`
  - Optional query params:
    - `start_time` (inclusive, RFC3339)
    - `end_time` (inclusive, RFC3339)
  - Returns an array of `TelemetryRecord` JSON objects ordered by `timestamp`.

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

### Prerequisites

- Go 1.23+ (toolchain set to Go 1.24 in `go.mod`).
- Docker.
- kind (Kubernetes in Docker).
- kubectl.
- helm.

### Go Build / Test

From repo root (`/Users/varunv/go/src/varunv/gpu`):

```bash
# Run all unit tests
go test ./...

# Run focused coverage on core services
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
docker build -f Dockerfile.mqbroker  -t gpu-telemetry-mqbroker:latest .
docker build -f Dockerfile.streamer  -t gpu-telemetry-streamer:latest .
docker build -f Dockerfile.collector -t gpu-telemetry-collector:latest .
docker build -f Dockerfile.api       -t gpu-telemetry-api:latest .
```

For use with kind:

```bash
kind load docker-image gpu-telemetry-mqbroker:latest --name gpu
kind load docker-image gpu-telemetry-streamer:latest  --name gpu
kind load docker-image gpu-telemetry-collector:latest --name gpu
kind load docker-image gpu-telemetry-api:latest       --name gpu
```

### OpenAPI Generation

To regenerate a standalone `api/openapi.json` file from the code:

```bash
# Uses cmd/api-openapi to print the spec to stdout
go run ./cmd/api-openapi > api/openapi.json
```

---

## Helm Chart and Installation Workflow

### Chart Layout

- `helm/telemetry-pipeline/Chart.yaml` – metadata.
- `helm/telemetry-pipeline/values.yaml` – configurable values:
  - `mqBroker` – image, replicas, queueCapacity.
  - `streamer` – image, replicas, sleepMillis.
  - `collector` – image, replicas, workers.
  - `api` – image, replicas.
  - `postgres` – image, credentials, DB name, port.
- `helm/telemetry-pipeline/templates/`:
  - `postgres-configmap.yaml` – `init.sql` to create `telemetry` table.
  - `postgres-deployment.yaml`, `postgres-service.yaml`.
  - `mqbroker-deployment.yaml`, `mqbroker-service.yaml`.
  - `streamer-deployment.yaml`.
  - `collector-deployment.yaml`.
  - `api-deployment.yaml`, `api-service.yaml`.

### 1. Create kind Cluster

```bash
kind create cluster --name gpu
kubectl config use-context kind-gpu
```

### 2. Load Images into kind

From repo root:

```bash
docker build -f Dockerfile.mqbroker  -t gpu-telemetry-mqbroker:latest .
docker build -f Dockerfile.streamer  -t gpu-telemetry-streamer:latest .
docker build -f Dockerfile.collector -t gpu-telemetry-collector:latest .
docker build -f Dockerfile.api       -t gpu-telemetry-api:latest .

kind load docker-image gpu-telemetry-mqbroker:latest --name gpu
kind load docker-image gpu-telemetry-streamer:latest  --name gpu
kind load docker-image gpu-telemetry-collector:latest --name gpu
kind load docker-image gpu-telemetry-api:latest       --name gpu
```

### 3. Install Helm Chart

```bash
cd helm/telemetry-pipeline
helm install telemetry-pipeline .
```

Watch pods come up:

```bash
kubectl get pods
```

You should see:

- `telemetry-postgres`
- `telemetry-mq`
- `telemetry-streamer`
- `telemetry-collector`
- `telemetry-api`

### 4. Scaling Streamers and Collectors

- Scale streamers (more producers):

```bash
kubectl scale deploy/telemetry-streamer --replicas=4
```

- Scale collectors (more consumers):

```bash
kubectl scale deploy/telemetry-collector --replicas=4
```

No config change is needed: the shared `stream_progress` in Postgres ensures streamers don’t duplicate rows, and the MQ broker ensures collectors share work.

---

## Sample User Workflow

1. **Bootstrap the cluster**
   - Create kind cluster.
   - Build & load images.
   - `helm install telemetry-pipeline ./helm/telemetry-pipeline`.

2. **Verify telemetry ingestion**

   - Check pods:

     ```bash
     kubectl get pods
     ```

   - Confirm `telemetry-streamer`, `telemetry-collector`, `telemetry-mq`, and `telemetry-postgres` are `Running`.

3. **Explore the API via Swagger**

   - Port‑forward the API:

     ```bash
     kubectl port-forward svc/telemetry-api 8080:80
     ```

   - Open in browser:
     - Swagger UI: `http://localhost:8080/swagger`
     - OpenAPI JSON: `http://localhost:8080/openapi.json`

   - In Swagger UI, try:
     - `GET /api/v1/gpus` to see which GPUs have telemetry.
     - `GET /api/v1/gpus/{id}/telemetry` with optional `start_time` / `end_time` filters.

4. **Scale for load**

   - Increase producers/consumers:

     ```bash
     kubectl scale deploy/telemetry-streamer --replicas=4
     kubectl scale deploy/telemetry-collector --replicas=4
     ```

   - Streamers will cooperatively read from the CSV via the shared Postgres cursor and push into MQ; collectors will compete for those messages and persist them.

5. **Inspect data directly in Postgres (optional)**

   - Port‑forward Postgres:

     ```bash
     kubectl port-forward svc/telemetry-postgres 5432:5432
     ```

   - Connect with `psql`:

     ```bash
     psql "postgres://gpu:gpu-password@localhost:5432/gpu?sslmode=disable"
     SELECT COUNT(*) FROM telemetry;
     SELECT DISTINCT gpu_id FROM telemetry;
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


