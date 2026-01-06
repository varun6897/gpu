package collector

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/varunv/gpu/mq"
	"github.com/varunv/gpu/telemetry"
)

func TestCollectorConsumesAndPersists(t *testing.T) {
	q := mq.NewInMemoryQueue(10)
	store := NewInMemoryStore()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Publish a few telemetry records.
	for i := 0; i < 5; i++ {
		rec := telemetry.Record{
			Timestamp:  time.Now().UTC(),
			MetricName: "DCGM_FI_DEV_GPU_UTIL",
			GPUId:      "gpu-0",
			Value:      "42",
		}
		payload, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("failed to marshal record: %v", err)
		}
		if err := q.Publish(ctx, mq.Message{
			ID:      "msg",
			Key:     rec.GPUId,
			Payload: payload,
		}); err != nil {
			t.Fatalf("Publish failed: %v", err)
		}
	}

	// Close the queue so the collector exits after draining.
	q.Close()

	if err := Run(ctx, Config{
		Queue:   q,
		Store:   store,
		Workers: 2,
	}); err != nil {
		t.Fatalf("collector Run failed: %v", err)
	}

	records := store.QueryByGPU("gpu-0", time.Time{}, time.Time{})
	if len(records) != 5 {
		t.Fatalf("expected 5 records, got %d", len(records))
	}
}

func TestInMemoryStoreListAndQueryWithWindow(t *testing.T) {
	store := NewInMemoryStore()
	now := time.Now().UTC()

	ctx := context.Background()
	recs := []telemetry.Record{
		{Timestamp: now.Add(-2 * time.Minute), GPUId: "0"},
		{Timestamp: now.Add(-1 * time.Minute), GPUId: "0"},
		{Timestamp: now, GPUId: "1"},
	}
	for _, r := range recs {
		if err := store.Save(ctx, r); err != nil {
			t.Fatalf("Save failed: %v", err)
		}
	}

	gpus := store.ListGPUs()
	if len(gpus) != 2 {
		t.Fatalf("expected 2 GPUs, got %d", len(gpus))
	}

	start := now.Add(-90 * time.Second)
	end := now.Add(-30 * time.Second)
	windowed := store.QueryByGPU("0", start, end)
	if len(windowed) != 1 {
		t.Fatalf("expected 1 record in window, got %d", len(windowed))
	}
}

type errorQueue struct {
	err error
}

func (e *errorQueue) Publish(context.Context, mq.Message) error { return nil }
func (e *errorQueue) Consume(context.Context) (mq.Message, error) {
	return mq.Message{}, e.err
}
func (e *errorQueue) Close() {}

type errorStore struct{}

func (e *errorStore) Save(context.Context, telemetry.Record) error { return errors.New("save error") }

type singleMessageQueue struct {
	msgs []mq.Message
	i    int
}

func (s *singleMessageQueue) Publish(context.Context, mq.Message) error { return nil }
func (s *singleMessageQueue) Consume(context.Context) (mq.Message, error) {
	if s.i < len(s.msgs) {
		m := s.msgs[s.i]
		s.i++
		return m, nil
	}
	return mq.Message{}, mq.ErrClosed
}
func (s *singleMessageQueue) Close() {}

func TestCollectorRunQueueClosedIsClean(t *testing.T) {
	q := &errorQueue{err: mq.ErrClosed}
	store := NewInMemoryStore()

	if err := Run(context.Background(), Config{
		Queue:   q,
		Store:   store,
		Workers: 2,
	}); err != nil {
		t.Fatalf("expected nil error when queue is closed, got %v", err)
	}
}

func TestCollectorRunPropagatesQueueError(t *testing.T) {
	q := &errorQueue{err: errors.New("boom")}
	store := NewInMemoryStore()

	if err := Run(context.Background(), Config{
		Queue:   q,
		Store:   store,
		Workers: 1,
	}); err == nil {
		t.Fatalf("expected error from Run when queue returns error")
	}
}

func TestCollectorRunStoreError(t *testing.T) {
	// Queue returns a single valid message then ErrClosed.
	sm := &singleMessageQueue{}
	rec := telemetry.Record{GPUId: "0"}
	payload, _ := json.Marshal(rec)
	sm.msgs = []mq.Message{{Payload: payload}}

	queue := sm

	if err := Run(context.Background(), Config{
		Queue:   queue,
		Store:   &errorStore{},
		Workers: 1,
	}); err == nil {
		t.Fatalf("expected error from Run when Store.Save fails")
	}
}
