## AI Assistance and Development Workflow

This document describes how AI assistance was used throughout the development of the GPU Telemetry Pipeline project, and which parts were performed manually. It is organized by phase and by the specific aspects requested in the exercise.

---

## 1. Project / Repo Bootstrapping

### Initial Requirements and Architecture

**User inputs (manual):**

- You provided the problem statement as the PDF `GPU Telemetry Pipeline Message Queue.pdf`, which specified:
  - Custom message queue (no Kafka/RabbitMQ/etc.).
  - Telemetry Streamer, Collector, API Gateway.
  - Kubernetes + Helm deployment.
  - OpenAPI spec autogeneration.
  - Unit tests and coverage.

**AI assistance:**

- You asked the AI to:
  - Read and interpret the PDF specification.
  - Propose how to implement the messaging queue in Go.
  - Propose a design for the overall system architecture.

- The AI suggested:
  - A `mq.Queue` interface with an `InMemoryQueue` implementation.
  - Separate `streamer`, `collector`, `api`, and `mqbroker` services.
  - Using Postgres as a persistence layer and as a potential coordination store.
  - Helm charts for deploying all components to a kind cluster.

**Prompts (examples, paraphrased and excerpted):**

- *“GPU Telemetry Pipeline Message Queue.pdf I need you to go through this and see what is needed to create the messaging queue using go”*
- *“Go ahead with the go code for the same”*
- *“Create a Telemetry Collector: consumes telemetry from the custom message queue, parses and persists it. The implementation should support the ability to dynamically scale up/down the number of Collectors.”*

**Manual work:**

- You created the Go module (`go.mod`) and placed the initial CSV and PDF into the repo.
- You ran `go test`, Docker builds, and Helm/kubectl commands in your local environment as instructed.

---

## 2. Code Bootstrapping

This includes all application code for the message queue, streamer, collector, API gateway, and broker, along with supporting packages.

### 2.1 Message Queue (`mq` package and `mqbroker`)

**AI‑generated / accelerated:**

- `mq/queue.go`:
  - Defined `Message` struct (ID, Key, Payload, Enqueued, Metadata).
  - Defined `Queue` interface (`Publish`, `Consume`, `Close`, `ErrClosed`).
  - Implemented `InMemoryQueue` using `sync.Mutex` and `sync.Cond` for:
    - Concurrency safety.
    - Bounded capacity.
    - Context cancellation (`waitWithContext`).
- `mq/http_queue.go`:
  - Implemented `HTTPQueue` that satisfies `mq.Queue` by calling a remote broker over HTTP.
  - Defined JSON wire format (`wireMessage`) and context-aware `Publish`/`Consume`.
- `cmd/mqbroker/main.go`:
  - HTTP service exposing:
    - `POST /publish` → `queue.Publish`.
    - `POST /consume` → `queue.Consume`.
    - `GET /healthz`.
  - Wiring of `InMemoryQueue` with configurable `QUEUE_CAPACITY`.

**Prompts that drove this:**

- *“Use this in a telemetry streamer that is present in a new directory called streamer where we read the csv on loop and change the date to current date and send this to the queue as mentioned GPU Telemetry Pipeline Message Queue.pdf”*
- *“Create a Telemetry Collector: consumes telemetry from the custom message queue, parses and persists it. The implementation should support the ability to dynamically scale up/down the number of Collectors.”*
- *“Go ahead with creation of mq broker service which is a custom individual messaging queue thats in memory and also streamer and collector”*

**Manual work:**

- You ran the builds, fixed any local environment issues (e.g., Go toolchain, Docker).
- You adjusted `go.mod` and accepted/reviewed AI changes before committing.

### 2.2 Streamer (`streamer` package and `cmd/streamer`)

**Initial AI‑generated streamer:**

- `streamer/streamer.go`:
  - `TelemetryRecord` (later refactored to `telemetry.Record`).
  - `Config` with `CSVPath`, `Queue`, `Loop`, `SleepBetweenRecords`, `ShardCount`, `ShardIndex`.
  - `Run(ctx, cfg)` that:
    - Read CSV row by row.
    - Replaced CSV timestamp with `time.Now().UTC()`.
    - JSON‑encoded each record and published it to `mq.Queue`.
    - Used `ownsRecord(gpuID, ShardIndex, ShardCount)` (fnv hash) to implement sharding.

**Refactoring to shared work index (AI‑guided):**

- Based on your scaling requirements:
  - *“I want the pods to keep track of the last row shared to mq and then share the next one”*
  - *“I want streamer to be scalable with just kubectl scale deploy and make sure the pods arent reading the same data... I want it to work like how worker groups work in golang”*

- AI proposed and implemented:
  - Postgres table `stream_progress(stream_id TEXT PRIMARY KEY, next_row BIGINT NOT NULL)`.
  - `initStreamProgress` and `nextRowIndex` functions in `cmd/streamer/main.go`.
  - New `runShared(ctx, db, streamID, rows, queue, sleep)` that:
    - Loads CSV into memory once.
    - Calls `nextRowIndex` to get a unique index for each “job.”
    - Maps `idx` to `rows[idx % len(rows)]` to loop the CSV.
    - Constructs `telemetry.Record` and publishes via `mq.HTTPQueue`.
  - Updated Helm template (`streamer-deployment.yaml`) to wire `POSTGRES_DSN` into streamer pods.

**Manual work:**

- You validated that multiple streamer pods were running and that no duplicates were produced.
- You confirmed logs and behavior in your kind cluster.

### 2.3 Collector (`collector` package and `cmd/collector`)

**AI‑generated / accelerated:**

- `collector/collector.go`:
  - `Store` interface (with `Save`).
  - `InMemoryStore` with `Save`, `ListGPUs`, `QueryByGPU`.
  - `Config` and `Run` function:
    - Spawns `Workers` goroutines.
    - Each worker calls `runWorker` in a loop:
      - `q.Consume(ctx)` from `mq.Queue`.
      - JSON‑unmarshal into `telemetry.Record`.
      - `store.Save(ctx, rec)`.
    - Cleanly handles `mq.ErrClosed` vs. other errors.
- `collector/postgres_store.go`:
  - `PostgresStore` with:
    - `Save` → `INSERT INTO telemetry(...)`.
    - `ListGPUs` → `SELECT DISTINCT gpu_id FROM telemetry`.
    - `QueryByGPU` → parameterized query with optional time window and `ORDER BY timestamp ASC`.
- `cmd/collector/main.go`:
  - Accepts `POSTGRES_DSN`, `MQ_BASE_URL`, and `COLLECTOR_WORKERS`.
  - Connects to Postgres, builds `PostgresStore`, uses `mq.NewHTTPQueue`, and calls `collector.Run`.

**Prompts that drove this:**

- *“Make this telemetry persist this data in a postgres db thats running in the cluster”*
- *“Can it be scaled to 10 streamers running as individual pods reading form the same csv without reading the same data ?”* (led to more robust collector design and queue semantics).

**Manual work:**

- You connected to Postgres externally to inspect the `telemetry` table.
- You scaled collector deployments with `kubectl scale` and verified ingestion rate.

### 2.4 API Gateway (`api` package and `cmd/api`)

**AI‑generated / accelerated:**

- `api/server.go`:
  - `Store` interface with `ListGPUs` and `QueryByGPU`.
  - `Server` type built on `http.ServeMux` with routes:
    - `/healthz`
    - `/api/v1/gpus`
    - `/api/v1/gpus/` (for `/api/v1/gpus/{id}/telemetry`)
    - `/openapi.json`
    - `/swagger` (Swagger UI).
  - Handlers:
    - `handleHealth` – 200 on GET, 405 on others.
    - `handleListGPUs` – returns JSON array of GPU IDs, handles method/Store error paths.
    - `handleTelemetryByGPU` – parses path and query parameters, validates RFC3339 timestamps, handles Store errors, returns telemetry records.
    - `BuildOpenAPISpec` – constructs a minimal OpenAPI 3.0 document in Go.
    - `handleOpenAPI` – returns the JSON spec.
    - `handleSwaggerUI` – serves a static HTML page embedding Swagger UI via CDN.

- `cmd/api/main.go`:
  - Reads `POSTGRES_DSN`, connects to Postgres.
  - Creates `collector.PostgresStore`.
  - Starts HTTP server on `LISTEN_ADDR` (default `:8080`) using `api.NewServer(store)`.

**Prompts:**

- *“Add a HTTP API service where API Gateway: REST API exposing telemetry. The OpenApi spec for the REST API should be auto generated ... endpoints: /api/v1/gpus, /api/v1/gpus/{id}/telemetry”*
- *“for the api I also want to add openapi so I can test it from there”* (led to `/swagger` UI).

**Manual work:**

- You confirmed the API deployment and service via `kubectl get pods` / `kubectl get svc`.
- You ran `kubectl port-forward svc/telemetry-api 8080:80` and verified:
  - `http://localhost:8080/openapi.json`
  - `http://localhost:8080/swagger`

---

## 3. Unit Tests and Coverage

### 3.1 AI‑Assisted Test Development

You requested:

- *“Can you add unit tests to all the services and make sure that it covers at least 80%”*
- *“Add tests for the errors as well”*

AI then:

- Wrote tests for:
  - `mq/queue.go` (`TestPublishAndConsumeSingle`, `TestConcurrentPublishConsume`, `TestClose`).
  - Additional error-path tests:
    - `TestPublishContextCancelledWhileWaiting` and `TestConsumeContextCancelledWhileWaiting` for `waitWithContext`.
  - `mq/http_queue.go`:
    - `TestHTTPQueuePublishConsume` (happy path).
    - `TestHTTPQueueErrorStatuses` (500 and 410 handling).
  - `streamer/streamer.go`:
    - `TestRunStreamsRecordsOnce`.
    - `TestShardingDistributesRecordsWithoutDuplication`.
    - `TestRewindCSV`.
    - Additional tests for error and edge paths:
      - `TestRunShardSkip` (sharding skip behavior).
      - `TestRunPublishError` (queue errors).
      - `TestRunImmediateCancel` (context cancellation).
  - `collector/collector.go` and `collector/postgres_store.go`:
    - `TestCollectorConsumesAndPersists`.
    - `TestInMemoryStoreListAndQueryWithWindow`.
    - Postgres store tests using `sqlmock`:
      - `TestPostgresStoreSave`.
      - `TestPostgresStoreListAndQuery`.
    - Error-path tests:
      - `TestCollectorRunQueueClosedIsClean`.
      - `TestCollectorRunPropagatesQueueError`.
      - `TestCollectorRunStoreError`.
  - `api/server.go`:
    - `TestHandleListGPUs`.
    - `TestHandleTelemetryByGPU_WithTimeFilters`.
    - `TestHandleTelemetryByGPU_InvalidTime`.
    - `TestHandleOpenAPI`.
    - `TestHandleHealth_Methods`.
    - Error-path and method tests:
      - `TestHandleListGPUs_ErrorAndMethod`.
      - `TestHandleTelemetryByGPU_ErrorAndNotFoundAndMethod`.
      - `TestHandleOpenAPI_MethodNotAllowed`.

- Iteratively ran:

  ```bash
  go test ./api ./collector ./mq ./streamer -coverprofile=coverage.out
  go tool cover -func=coverage.out
  ```

  And added tests for under‑covered functions until:
  - `api` coverage ≈ 96%.
  - `collector` coverage ≈ 80%.
  - `mq` coverage ≈ 80%+.
  - `streamer` coverage ≈ 70%.
  - Overall coverage across these packages > 80%.

### 3.2 Manual Contributions

- You triggered coverage commands, inspected coverage output, and requested additional error tests where you felt they were important.
- You verified that tests were meaningful for the system’s behavior, not just mechanical coverage improvements.

---

## 4. Build Environment and Deployment Bootstrapping

### 4.1 Build Environment (Go + Make)

**AI assistance:**

- Provided standard Go test and coverage commands.
- Added a small `Makefile`:

  - `test` → `go test ./...`
  - `coverage` → `go test ./api ./collector ./mq ./streamer -coverprofile=coverage.out` + `go tool cover -func=coverage.out`
  - `openapi` → `go run ./cmd/api-openapi > api/openapi.json`

You discovered that `make` wasn’t installed in the sandbox environment, but on your local machine you can use the Makefile as intended.

### 4.2 Docker and kind

**AI assistance:**

- Provided Dockerfiles for:
  - `mqbroker`, `streamer`, `collector`, `api`, and the legacy `pipeline`.
- Provided commands to:
  - Build each image with `docker build`.
  - Load images into kind with `kind load docker-image ... --name gpu`.

**Manual work:**

- Installed Docker and kind locally.
- Ran image builds and confirmed they worked on your system.

### 4.3 Helm and Kubernetes Deployment

**AI assistance:**

- Created and evolved `helm/telemetry-pipeline` chart:
  - Defined `values.yaml` for per‑service configuration (images, replicas, worker counts).
  - Templates for:
    - Postgres deployment and init ConfigMap.
    - MQ broker deployment/service.
    - Streamer deployment (with `POSTGRES_DSN`, `MQ_BASE_URL`, `STREAMER_SLEEP_MILLIS`).
    - Collector deployment (with `POSTGRES_DSN`, `MQ_BASE_URL`, `COLLECTOR_WORKERS`).
    - API deployment/service (with `POSTGRES_DSN`).
  - Removed the old monolithic `pipeline` deployment when you requested separate services.
- Provided commands:

  ```bash
  helm install telemetry-pipeline ./helm/telemetry-pipeline
  helm upgrade --install telemetry-pipeline ./helm/telemetry-pipeline
  kubectl scale deploy/telemetry-streamer --replicas=N
  kubectl scale deploy/telemetry-collector --replicas=M
  ```

**Manual work:**

- You managed cluster lifecycle:
  - `kind create cluster --name gpu`
  - `kubectl config use-context kind-gpu`
  - `helm uninstall telemetry-pipeline` and re‑installs when needed.
- You port‑forwarded services for manual testing:
  - `telemetry-api` for Swagger.
  - `telemetry-postgres` for direct DB inspection.

---

## 5. Summary: AI vs Manual Responsibilities

**AI focused on:**

- Designing and implementing:
  - The `mq.Queue` abstraction and in‑memory implementation.
  - HTTP MQ broker and client (`mqbroker`, `HTTPQueue`).
  - Initial streamer and collector logic and later refactoring streamer to use a shared Postgres work index.
  - API Gateway and OpenAPI/Swagger support.
  - Helm templates for deploying all services.
  - Comprehensive unit tests and coverage improvements.
- Debugging and iterating based on compiler errors, linter outputs, and pod logs.

**You focused on:**

- Providing detailed requirements and architectural constraints from the PDF.
- Running and validating:
  - `go test`, coverage commands, Docker builds.
  - kind cluster creation and management.
  - Helm installs/upgrades and `kubectl` operations.
  - Manual end‑to‑end testing via Swagger and direct Postgres inspection.
- Guiding priorities (e.g., shifting from hash‑based sharding to shared work index, insisting on independent scaling of streamer/collector, and requiring 80%+ coverage).

Together, this human+AI workflow allowed the project to go from a blank Go module and problem statement to a fully working, test‑covered, Kubernetes‑deployable GPU telemetry pipeline with a custom message queue and API Gateway in a relatively short time.


