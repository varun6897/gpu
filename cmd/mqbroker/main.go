package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/varun6897/gpu/mq"
)

type brokerServer struct {
	queue      *mq.InMemoryQueue
	mu         sync.Mutex
	inFlight   map[string]inFlight
	visibility time.Duration
}

type inFlight struct {
	msg      mq.Message
	deadline time.Time
}

func main() {
	logger := log.New(os.Stdout, "[mqbroker] ", log.LstdFlags|log.Lmicroseconds|log.LUTC)

	capacity := getEnvInt("QUEUE_CAPACITY", 1000)
	addr := getenv("LISTEN_ADDR", ":8081")
	visibilitySec := getEnvInt("VISIBILITY_TIMEOUT_SECONDS", 30)
	if visibilitySec <= 0 {
		visibilitySec = 30
	}

	q := mq.NewInMemoryQueue(capacity)
	s := &brokerServer{
		queue:      q,
		inFlight:   make(map[string]inFlight),
		visibility: time.Duration(visibilitySec) * time.Second,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/publish", s.handlePublish)
	mux.HandleFunc("/consume", s.handleConsume)
	mux.HandleFunc("/ack", s.handleAck)

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	// Start background re-delivery loop for messages that were consumed
	// but not acknowledged within the visibility timeout.
	go s.requeueExpired(context.Background(), logger)

	logger.Printf("starting mq-broker on %s with capacity %d, visibility %s\n", addr, capacity, s.visibility)
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

	// Track message as in-flight for at-least-once delivery.
	if msg.ID != "" {
		s.mu.Lock()
		s.inFlight[msg.ID] = inFlight{
			msg:      msg,
			deadline: time.Now().Add(s.visibility),
		}
		s.mu.Unlock()
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

// handleAck acknowledges that a message with the given ID has been
// successfully processed. It removes the message from the in-flight
// map so it will not be re-delivered.
func (s *brokerServer) handleAck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	delete(s.inFlight, body.ID)
	s.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

// requeueExpired periodically scans in-flight messages and re-enqueues
// any that have exceeded their visibility timeout. This provides
// at-least-once delivery semantics within the lifetime of the broker
// process.
func (s *brokerServer) requeueExpired(ctx context.Context, logger *log.Logger) {
	ticker := time.NewTicker(s.visibility / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			var toRequeue []mq.Message

			s.mu.Lock()
			for id, inf := range s.inFlight {
				if now.After(inf.deadline) {
					toRequeue = append(toRequeue, inf.msg)
					delete(s.inFlight, id)
				}
			}
			s.mu.Unlock()

			for _, m := range toRequeue {
				if err := s.queue.Publish(context.Background(), m); err != nil {
					logger.Printf("mq-broker: failed to requeue message %s: %v", m.ID, err)
				}
			}
		}
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
