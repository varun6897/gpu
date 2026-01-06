.PHONY: test coverage openapi

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


