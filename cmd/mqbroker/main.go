package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/varunv/gpu/mq"
)

type brokerServer struct {
	queue *mq.InMemoryQueue
}

func main() {
	logger := log.New(os.Stdout, "[mqbroker] ", log.LstdFlags|log.Lmicroseconds|log.LUTC)

	capacity := getEnvInt("QUEUE_CAPACITY", 1000)
	addr := getenv("LISTEN_ADDR", ":8081")

	q := mq.NewInMemoryQueue(capacity)
	s := &brokerServer{queue: q}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/publish", s.handlePublish)
	mux.HandleFunc("/consume", s.handleConsume)

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	logger.Printf("starting mq-broker on %s with capacity %d\n", addr, capacity)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("mq-broker server error: %v", err)
	}
}

func (s *brokerServer) handlePublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var wm struct {
		ID       string            `json:"id"`
		Key      string            `json:"key"`
		Payload  string            `json:"payload"`
		Metadata map[string]string `json:"metadata,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&wm); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	msg := mq.Message{
		ID:       wm.ID,
		Key:      wm.Key,
		Payload:  []byte(wm.Payload),
		Metadata: wm.Metadata,
	}
	if err := s.queue.Publish(r.Context(), msg); err != nil {
		if err == mq.ErrClosed {
			http.Error(w, "queue closed", http.StatusGone)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *brokerServer) handleConsume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()

	msg, err := s.queue.Consume(ctx)
	if err != nil {
		if err == mq.ErrClosed {
			http.Error(w, "queue closed", http.StatusGone)
			return
		}
		if ctx.Err() != nil {
			http.Error(w, "request cancelled", http.StatusRequestTimeout)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	wm := struct {
		ID       string            `json:"id"`
		Key      string            `json:"key"`
		Payload  string            `json:"payload"`
		Enqueued string            `json:"enqueued,omitempty"`
		Metadata map[string]string `json:"metadata,omitempty"`
	}{
		ID:       msg.ID,
		Key:      msg.Key,
		Payload:  string(msg.Payload),
		Metadata: msg.Metadata,
	}
	if !msg.Enqueued.IsZero() {
		wm.Enqueued = msg.Enqueued.Format(time.RFC3339Nano)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(&wm); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}


