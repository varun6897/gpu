.PHONY: test coverage openapi \
	kind-up kind-down build-images load-images helm-deploy deploy cleanup \
	quickstart-deploy quickstart-clean quickstart-test quickstart-coverage

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

# Name of the local kind cluster used for development.
KIND_CLUSTER_NAME ?= gpu

# Name of the Helm release and chart path for this project.
HELM_RELEASE ?= telemetry-pipeline
HELM_CHART_PATH ?= ./helm/telemetry-pipeline

# ---------------------------------------------------------------------------
# Testing / Coverage / OpenAPI
# ---------------------------------------------------------------------------

test:
	go test ./...

# Run coverage over the core service packages (excluding tiny cmd mains).
coverage:
	go test ./api ./collector ./mq ./streamer -coverprofile=coverage.out
	go tool cover -func=coverage.out

# Generate the OpenAPI spec by running the API server's OpenAPI builder.
# This writes openapi.json into the api/ directory.
openapi:
	go test ./api -run TestDoesNotExist -v >/dev/null 2>&1 || true
	go run ./cmd/api-openapi > api/openapi.json


# Convenience aliases for quick starts.
quickstart-test: test

quickstart-coverage: coverage


# ---------------------------------------------------------------------------
# kind + Docker + Helm: local deployment helpers
# ---------------------------------------------------------------------------

# Ensure a kind cluster exists and kubectl is pointed at it.
kind-up:
	kind get clusters | grep -qx '$(KIND_CLUSTER_NAME)' || kind create cluster --name $(KIND_CLUSTER_NAME)
	kubectl config use-context kind-$(KIND_CLUSTER_NAME)

# Tear down the kind cluster (safe to run even if it does not exist).
kind-down:
	-kind delete cluster --name $(KIND_CLUSTER_NAME) || true

# Build all service images used by the Helm chart.
build-images:
	docker build -f Dockerfile.mqbroker  -t gpu-telemetry-mqbroker:latest .
	docker build -f Dockerfile.streamer  -t gpu-telemetry-streamer:latest .
	docker build -f Dockerfile.collector -t gpu-telemetry-collector:latest .
	docker build -f Dockerfile.api       -t gpu-telemetry-api:latest .

# Load the built images into the kind node so no external registry is needed.
load-images:
	kind load docker-image gpu-telemetry-mqbroker:latest --name $(KIND_CLUSTER_NAME)
	kind load docker-image gpu-telemetry-streamer:latest  --name $(KIND_CLUSTER_NAME)
	kind load docker-image gpu-telemetry-collector:latest --name $(KIND_CLUSTER_NAME)
	kind load docker-image gpu-telemetry-api:latest       --name $(KIND_CLUSTER_NAME)

# Install / upgrade the Helm chart into the current cluster.
helm-deploy:
	helm upgrade --install $(HELM_RELEASE) $(HELM_CHART_PATH)
	kubectl get pods

# One-shot local deployment: create kind (if needed), build+load images, deploy Helm.
deploy: kind-up build-images load-images helm-deploy

# Clean up local deployment: uninstall Helm release and delete kind cluster.
cleanup:
	-helm uninstall $(HELM_RELEASE) || true
	-kind delete cluster --name $(KIND_CLUSTER_NAME) || true

# Quick-start aliases for deployment lifecycle.
quickstart-deploy: deploy

quickstart-clean: cleanup


