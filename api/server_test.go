package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/varun6897/gpu/telemetry"
)

type fakeStore struct {
	gpus      []telemetry.GPUInfo
	records   map[string][]telemetry.Record
	lastStart time.Time
	lastEnd   time.Time
	listErr   error
	queryErr  error
}

func (f *fakeStore) ListGPUs() ([]telemetry.GPUInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.gpus, nil
}

func (f *fakeStore) QueryByGPU(gpuID string, start, end time.Time) ([]telemetry.Record, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	f.lastStart = start
	f.lastEnd = end
	return f.records[gpuID], nil
}

func TestHandleListGPUs(t *testing.T) {
	fs := &fakeStore{gpus: []telemetry.GPUInfo{
		{UUID: "uuid0", Device: "dev0", ModelName: "model0"},
		{UUID: "uuid1", Device: "dev1", ModelName: "model1"},
	}}
	srv := NewServer(fs)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/gpus", nil)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var got []telemetry.GPUInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if len(got) != 2 || got[0].UUID != "uuid0" || got[0].Device != "dev0" || got[0].ModelName != "model0" {
		t.Fatalf("unexpected GPUs: %#v", got)
	}
}

func TestHandleTelemetryByGPU_WithTimeFilters(t *testing.T) {
	start := time.Date(2025, 7, 18, 20, 42, 34, 0, time.UTC)
	end := start.Add(time.Minute)

	rec := telemetry.Record{
		Timestamp:  start.Add(10 * time.Second),
		MetricName: "DCGM_FI_DEV_GPU_UTIL",
		GPUId:      "0",
		Value:      "42",
	}

	fs := &fakeStore{
		records: map[string][]telemetry.Record{
			"0": {rec},
		},
	}
	srv := NewServer(fs)

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/gpus/0/telemetry?start_time="+start.Format(time.RFC3339)+"&end_time="+end.Format(time.RFC3339),
		nil,
	)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	// Verify the store saw the same window.
	if !fs.lastStart.Equal(start) || !fs.lastEnd.Equal(end) {
		t.Fatalf("expected start=%v,end=%v, got start=%v,end=%v", start, end, fs.lastStart, fs.lastEnd)
	}

	var got []telemetry.Record
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if len(got) != 1 || got[0].GPUId != "0" || got[0].Value != "42" {
		t.Fatalf("unexpected records: %#v", got)
	}
}

func TestHandleTelemetryByGPU_InvalidTime(t *testing.T) {
	fs := &fakeStore{}
	srv := NewServer(fs)

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/gpus/0/telemetry?start_time=not-a-time",
		nil,
	)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid start_time, got %d", rr.Code)
	}
}

func TestHandleOpenAPI(t *testing.T) {
	fs := &fakeStore{}
	srv := NewServer(fs)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var doc map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("failed to unmarshal openapi: %v", err)
	}
	if doc["openapi"] == "" {
		t.Fatalf("expected openapi version in spec, got: %#v", doc)
	}
}

func TestHandleHealth_Methods(t *testing.T) {
	fs := &fakeStore{}
	srv := NewServer(fs)

	// GET should succeed.
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /healthz expected 200, got %d", rr.Code)
	}

	// POST should be method not allowed.
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rr2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz expected 405, got %d", rr2.Code)
	}
}

func TestHandleListGPUs_ErrorAndMethod(t *testing.T) {
	fs := &fakeStore{listErr: fmt.Errorf("boom")}
	srv := NewServer(fs)

	// Store error -> 500
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/gpus", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on store error, got %d", rr.Code)
	}

	// Wrong method -> 405
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, httptest.NewRequest(http.MethodPost, "/api/v1/gpus", nil))
	if rr2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 on wrong method, got %d", rr2.Code)
	}
}

func TestHandleTelemetryByGPU_ErrorAndNotFoundAndMethod(t *testing.T) {
	fs := &fakeStore{queryErr: fmt.Errorf("boom")}
	srv := NewServer(fs)

	// Store error -> 500
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/gpus/0/telemetry", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on store error, got %d", rr.Code)
	}

	// Bad path -> 404
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/api/v1/gpus/0/metrics", nil))
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for bad path, got %d", rr2.Code)
	}

	// Wrong method -> 405
	rr3 := httptest.NewRecorder()
	srv.ServeHTTP(rr3, httptest.NewRequest(http.MethodPost, "/api/v1/gpus/0/telemetry", nil))
	if rr3.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for wrong method, got %d", rr3.Code)
	}
}

func TestHandleOpenAPI_MethodNotAllowed(t *testing.T) {
	fs := &fakeStore{}
	srv := NewServer(fs)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/openapi.json", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for POST /openapi.json, got %d", rr.Code)
	}
}
