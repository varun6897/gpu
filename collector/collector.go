package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/varunv/gpu/mq"
	"github.com/varunv/gpu/telemetry"
)

// Store is the abstraction for telemetry persistence.
// Implementations must be safe for concurrent use by multiple goroutines.
type Store interface {
	Save(ctx context.Context, rec telemetry.Record) error
}

// InMemoryStore is a simple thread-safe in-memory implementation of Store.
// It is suitable for the exercise and can be swapped for a persistent store
// (e.g. PostgreSQL) without changing collectors.
type InMemoryStore struct {
	mu   sync.RWMutex
	data map[string][]telemetry.Record // key: GPUId
}

// NewInMemoryStore constructs an empty in-memory telemetry store.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		data: make(map[string][]telemetry.Record),
	}
}

// Save appends a record to the in-memory store.
func (s *InMemoryStore) Save(_ context.Context, rec telemetry.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[rec.GPUId] = append(s.data[rec.GPUId], rec)
	return nil
}

// ListGPUs returns the set of GPU IDs for which telemetry is stored.
func (s *InMemoryStore) ListGPUs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.data))
	for id := range s.data {
		ids = append(ids, id)
	}
	return ids
}

// QueryByGPU returns telemetry records for a given GPU ID, optionally
// constrained to a [start, end] time window (inclusive). If both start and
// end are zero, all records are returned. The returned slice is a copy and
// safe for callers to modify.
func (s *InMemoryStore) QueryByGPU(gpuID string, start, end time.Time) []telemetry.Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.data[gpuID]
	if len(src) == 0 {
		return nil
	}
	out := make([]telemetry.Record, 0, len(src))
	for _, rec := range src {
		if !start.IsZero() && rec.Timestamp.Before(start) {
			continue
		}
		if !end.IsZero() && rec.Timestamp.After(end) {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// Config controls how a collector consumes telemetry from the message queue
// and persists it using the configured Store.
type Config struct {
	// Queue is the source message queue to consume telemetry from.
	Queue mq.Queue
	// Store is where parsed telemetry records are persisted.
	Store Store
	// Workers controls how many concurrent consumer goroutines are used
	// within this collector instance. If <= 0, a default of 1 is used.
	Workers int
}

// Run starts one or more worker goroutines that continuously consume telemetry
// messages from the queue, parse them, and persist them into the Store.
//
// Scaling:
//   - Within a single process/pod, increase cfg.Workers to scale vertically.
//   - To scale horizontally, run multiple collector instances (pods) pointing
//     at the same logical Queue service; they will naturally compete for and
//     share work thanks to mq.Queue's competing-consumer semantics.
//
// Run blocks until:
//   - The provided context is cancelled, or
//   - The queue is closed and drained (mq.ErrClosed after draining), or
//   - A non-recoverable error occurs in any worker.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Queue == nil {
		return fmt.Errorf("collector: Queue is required")
	}
	if cfg.Store == nil {
		return fmt.Errorf("collector: Store is required")
	}
	workers := cfg.Workers
	if workers <= 0 {
		workers = 1
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			if err := runWorker(ctx, cfg.Queue, cfg.Store); err != nil {
				// mq.ErrClosed is treated as a clean shutdown signal.
				if err == mq.ErrClosed {
					return
				}
				select {
				case errCh <- err:
					cancel()
				default:
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)

	// Return the first non-nil error, if any.
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func runWorker(ctx context.Context, q mq.Queue, store Store) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msg, err := q.Consume(ctx)
		if err != nil {
			return err
		}

		var rec telemetry.Record
		if err := json.Unmarshal(msg.Payload, &rec); err != nil {
			// Malformed payload; skip and continue.
			continue
		}

		if err := store.Save(ctx, rec); err != nil {
			return err
		}
	}
}


