package mq

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestPublishAndConsumeSingle(t *testing.T) {
	q := NewInMemoryQueue(10)
	defer q.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	msg := Message{
		ID:      "1",
		Key:     "gpu-0",
		Payload: []byte("hello"),
	}

	if err := q.Publish(ctx, msg); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	got, err := q.Consume(ctx)
	if err != nil {
		t.Fatalf("Consume failed: %v", err)
	}

	if string(got.Payload) != "hello" || got.ID != "1" || got.Key != "gpu-0" {
		t.Fatalf("unexpected message: %#v", got)
	}
}

func TestConcurrentPublishConsume(t *testing.T) {
	q := NewInMemoryQueue(100)
	defer q.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	const total = 100

	var wg sync.WaitGroup
	wg.Add(2)

	// Publisher.
	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			msg := Message{
				ID:      string(rune('a' + (i % 26))),
				Key:     "gpu-0",
				Payload: []byte("msg"),
			}
			if err := q.Publish(ctx, msg); err != nil {
				return
			}
		}
	}()

	// Consumer.
	received := 0
	go func() {
		defer wg.Done()
		for {
			if received >= total {
				return
			}
			_, err := q.Consume(ctx)
			if err != nil {
				return
			}
			received++
		}
	}()

	wg.Wait()

	if received != total {
		t.Fatalf("expected %d messages, got %d", total, received)
	}
}

func TestClose(t *testing.T) {
	q := NewInMemoryQueue(1)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := q.Publish(ctx, Message{Payload: []byte("x")}); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	q.Close()

	// Consume the remaining message.
	if _, err := q.Consume(ctx); err != nil {
		t.Fatalf("Consume after close should succeed for remaining messages, got: %v", err)
	}

	// Now queue is closed and drained.
	if _, err := q.Consume(ctx); err != ErrClosed {
		t.Fatalf("expected ErrClosed after draining closed queue, got: %v", err)
	}

	// Further publishes should fail.
	if err := q.Publish(ctx, Message{Payload: []byte("y")}); err != ErrClosed {
		t.Fatalf("expected ErrClosed on Publish to closed queue, got: %v", err)
	}
}

func TestPublishContextCancelledWhileWaiting(t *testing.T) {
	q := NewInMemoryQueue(1)

	// Fill the queue.
	ctx := context.Background()
	if err := q.Publish(ctx, Message{Payload: []byte("first")}); err != nil {
		t.Fatalf("initial publish failed: %v", err)
	}

	// Second publish with an already-expired context should fail immediately via waitWithContext.
	ctxTimeout, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()

	err := q.Publish(ctxTimeout, Message{Payload: []byte("second")})
	if err == nil {
		t.Fatalf("expected error due to context deadline, got nil")
	}
}

func TestConsumeContextCancelledWhileWaiting(t *testing.T) {
	q := NewInMemoryQueue(1)
	// Empty queue; consume with already-expired context should fail via waitWithContext.
	ctxTimeout, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()

	if _, err := q.Consume(ctxTimeout); err == nil {
		t.Fatalf("expected error due to context deadline, got nil")
	}
}
