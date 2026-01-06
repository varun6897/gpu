package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/varunv/gpu/telemetry"
)

// Store is the subset of persistence methods needed by the HTTP API.
type Store interface {
	ListGPUs() ([]string, error)
	QueryByGPU(gpuID string, start, end time.Time) ([]telemetry.Record, error)
}

// Server implements the HTTP API Gateway for telemetry.
// It is safe to use concurrently.
type Server struct {
	store Store
	mux   *http.ServeMux
}

// NewServer constructs a Server with all routes registered on an internal mux.
func NewServer(store Store) *Server {
	s := &Server{
		store: store,
		mux:   http.NewServeMux(),
	}
	s.routes()
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealth)
	s.mux.HandleFunc("/api/v1/gpus", s.handleListGPUs)
	// Catch-all for GPU telemetry; we'll parse the path manually.
	s.mux.HandleFunc("/api/v1/gpus/", s.handleTelemetryByGPU)
	// OpenAPI specification endpoint.
	s.mux.HandleFunc("/openapi.json", s.handleOpenAPI)
	// Simple Swagger UI page that uses the generated OpenAPI spec.
	s.mux.HandleFunc("/swagger", s.handleSwaggerUI)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleListGPUs implements:
//   GET /api/v1/gpus
// Response: 200 JSON array of GPU ID strings.
func (s *Server) handleListGPUs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	gpus, err := s.store.ListGPUs()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, gpus)
}

// handleTelemetryByGPU implements:
//   GET /api/v1/gpus/{id}/telemetry?start_time=...&end_time=...
// where start_time and end_time are optional RFC3339 timestamps.
func (s *Server) handleTelemetryByGPU(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Path is expected to be /api/v1/gpus/{id}/telemetry
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/gpus/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != "telemetry" || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	gpuID := parts[0]

	var start, end time.Time
	var err error

	if v := r.URL.Query().Get("start_time"); v != "" {
		start, err = time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "invalid start_time, must be RFC3339", http.StatusBadRequest)
			return
		}
	}
	if v := r.URL.Query().Get("end_time"); v != "" {
		end, err = time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "invalid end_time, must be RFC3339", http.StatusBadRequest)
			return
		}
	}

	records, err := s.store.QueryByGPU(gpuID, start, end)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, records)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// BuildOpenAPISpec constructs a minimal OpenAPI 3.0 specification for this API.
// It is returned as a generic map so it can be serialized directly to JSON.
func BuildOpenAPISpec() map[string]any {
	return map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":   "GPU Telemetry API",
			"version": "v1",
		},
		"paths": map[string]any{
			"/api/v1/gpus": map[string]any{
				"get": map[string]any{
					"summary":     "List all GPUs",
					"description": "Return a list of all GPUs for which telemetry data is available.",
					"responses": map[string]any{
						"200": map[string]any{
							"description": "OK",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"type":  "array",
										"items": map[string]any{"type": "string"},
									},
								},
							},
						},
					},
				},
			},
			"/api/v1/gpus/{id}/telemetry": map[string]any{
				"get": map[string]any{
					"summary":     "Query telemetry by GPU",
					"description": "Return telemetry entries for a specific GPU, ordered by time. Optional start_time and end_time filters are supported.",
					"parameters": []any{
						map[string]any{
							"name":     "id",
							"in":       "path",
							"required": true,
							"schema":   map[string]any{"type": "string"},
						},
						map[string]any{
							"name":        "start_time",
							"in":          "query",
							"required":    false,
							"description": "Start of time window (inclusive), RFC3339 format",
							"schema":      map[string]any{"type": "string", "format": "date-time"},
						},
						map[string]any{
							"name":        "end_time",
							"in":          "query",
							"required":    false,
							"description": "End of time window (inclusive), RFC3339 format",
							"schema":      map[string]any{"type": "string", "format": "date-time"},
						},
					},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "OK",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"type": "array",
										"items": map[string]any{
											"$ref": "#/components/schemas/TelemetryRecord",
										},
									},
								},
							},
						},
					},
				},
			},
		},
		"components": map[string]any{
			"schemas": map[string]any{
				"TelemetryRecord": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"timestamp":   map[string]any{"type": "string", "format": "date-time"},
						"metric_name": map[string]any{"type": "string"},
						"gpu_id":      map[string]any{"type": "string"},
						"device":      map[string]any{"type": "string"},
						"uuid":        map[string]any{"type": "string"},
						"modelName":   map[string]any{"type": "string"},
						"hostname":    map[string]any{"type": "string"},
						"container":   map[string]any{"type": "string"},
						"pod":         map[string]any{"type": "string"},
						"namespace":   map[string]any{"type": "string"},
						"value":       map[string]any{"type": "string"},
						"labels_raw":  map[string]any{"type": "string"},
					},
				},
			},
		},
	}
}

// handleOpenAPI serves the OpenAPI specification as JSON at /openapi.json.
func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	doc := BuildOpenAPISpec()
	writeJSON(w, http.StatusOK, doc)
}

// handleSwaggerUI serves a minimal Swagger UI page that loads the OpenAPI
// document from /openapi.json so the API can be explored interactively.
func (s *Server) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html>
<html>
  <head>
    <meta charset="utf-8">
    <title>GPU Telemetry API - Swagger UI</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css" />
  </head>
  <body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
    <script>
      window.onload = function() {
        SwaggerUIBundle({
          url: '/openapi.json',
          dom_id: '#swagger-ui'
        });
      };
    </script>
  </body>
</html>`))
}




