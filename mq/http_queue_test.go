package mq

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHTTPQueuePublishConsume verifies that HTTPQueue correctly encodes and
// decodes messages against a minimal in-process HTTP broker.
func TestHTTPQueuePublishConsume(t *testing.T) {
	q := NewInMemoryQueue(10)

	mux := http.NewServeMux()
	mux.HandleFunc("/publish", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var wm wireMessage
		if err := json.NewDecoder(r.Body).Decode(&wm); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		msg := Message{
			ID:      wm.ID,
			Key:     wm.Key,
			Payload: []byte(wm.Payload),
		}
		if err := q.Publish(r.Context(), msg); err != nil {
			http.Error(w, "publish error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/consume", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		msg, err := q.Consume(r.Context())
		if err != nil {
			if err == ErrClosed {
				http.Error(w, "closed", http.StatusGone)
				return
			}
			http.Error(w, "consume error", http.StatusInternalServerError)
			return
		}
		wm := wireMessage{
			ID:      msg.ID,
			Key:     msg.Key,
			Payload: string(msg.Payload),
		}
		_ = json.NewEncoder(w).Encode(&wm)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	hq := NewHTTPQueue(srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	orig := Message{
		ID:      "1",
		Key:     "gpu-0",
		Payload: []byte("hello"),
	}
	if err := hq.Publish(ctx, orig); err != nil {
		t.Fatalf("Publish via HTTPQueue failed: %v", err)
	}

	got, err := hq.Consume(ctx)
	if err != nil {
		t.Fatalf("Consume via HTTPQueue failed: %v", err)
	}

	if got.ID != orig.ID || got.Key != orig.Key || string(got.Payload) != "hello" {
		t.Fatalf("unexpected message: %#v", got)
	}

	// Close should be a no-op but covered.
	hq.Close()
}

func TestHTTPQueueErrorStatuses(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/publish", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/consume", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	hq := NewHTTPQueue(srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := hq.Publish(ctx, Message{ID: "1", Key: "gpu-0", Payload: []byte("x")}); err == nil {
		t.Fatalf("expected error from Publish on 500 status")
	}

	if _, err := hq.Consume(ctx); err != ErrClosed {
		t.Fatalf("expected ErrClosed from Consume on 410 status, got %v", err)
	}
}



