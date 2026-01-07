package mq

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Message represents a single unit of data flowing through the queue.
// For telemetry, the Key should typically be the GPU ID so that downstream
// systems can reason about per-GPU ordering or sharding decisions.
type Message struct {
	ID       string
	Key      string
	Payload  []byte
	Enqueued time.Time
	Metadata map[string]string
}

// Queue defines the minimal contract for a point-to-point message queue.
//
// Semantics:
//   - Messages are delivered to consumers in FIFO order.
//   - Multiple concurrent consumers share work from the same queue
//     (competing consumer model).
//   - There is no persistence; this implementation is in-memory only.
//   - Delivery is at-most-once within a single process: once a message
//     is handed to a consumer, it is removed from the queue.
//
// This is sufficient for the GPU telemetry exercise and can be swapped out
// for a more sophisticated implementation without changing callers.
type Queue interface {
	// Publish enqueues a message for delivery.
	// It blocks if the queue is full and returns an error if the context
	// is cancelled before the message is enqueued or if the queue is closed.
	Publish(ctx context.Context, msg Message) error

	// Consume blocks until a message is available, the context is cancelled,
	// or the queue is closed. If the queue is closed and drained, it returns
	// (Message{}, ErrClosed).
	Consume(ctx context.Context) (Message, error)

	// Close marks the queue as closed. Further calls to Publish will fail
	// with ErrClosed. Pending messages can still be consumed until drained.
	Close()
}

// AckQueue is an optional extension of Queue that supports explicit
// acknowledgements for messages. Implementations typically provide
// at-least-once delivery semantics by re-delivering messages that are
// consumed but not acknowledged within some timeout window.
//
// Not all Queue implementations need to support this; callers can safely
// type-assert to AckQueue and, if the assertion fails, fall back to
// at-most-once behaviour.
type AckQueue interface {
	Queue

	// Ack acknowledges successful processing of the message with the
	// given ID. Implementations may use this to remove the message from
	// in-flight tracking and prevent it from being re-delivered.
	Ack(ctx context.Context, id string) error
}

var (
	// ErrClosed is returned when operating on a closed queue.
	ErrClosed = errors.New("mq: queue is closed")
)

// InMemoryQueue is a bounded in-memory implementation of Queue.
//
// It is safe for concurrent use by multiple publishers and consumers.
type InMemoryQueue struct {
	mu       sync.Mutex
	notEmpty *sync.Cond

	buf      []Message
	capacity int
	closed   bool
}

// NewInMemoryQueue constructs a new queue with the given capacity.
// Capacity must be > 0.
func NewInMemoryQueue(capacity int) *InMemoryQueue {
	if capacity <= 0 {
		capacity = 1
	}
	q := &InMemoryQueue{
		buf:      make([]Message, 0, capacity),
		capacity: capacity,
	}
	q.notEmpty = sync.NewCond(&q.mu)
	return q
}

// Publish implements Queue.Publish.
func (q *InMemoryQueue) Publish(ctx context.Context, msg Message) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return ErrClosed
	}

	// Block while the buffer is full, but respect context cancellation.
	for len(q.buf) >= q.capacity && !q.closed {
		if err := q.waitWithContext(ctx); err != nil {
			return err
		}
	}

	if q.closed {
		return ErrClosed
	}

	if msg.Enqueued.IsZero() {
		msg.Enqueued = time.Now()
	}

	q.buf = append(q.buf, msg)
	q.notEmpty.Signal()

	return nil
}

// Consume implements Queue.Consume.
func (q *InMemoryQueue) Consume(ctx context.Context) (Message, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.buf) == 0 && !q.closed {
		if err := q.waitWithContext(ctx); err != nil {
			return Message{}, err
		}
	}

	if len(q.buf) == 0 && q.closed {
		return Message{}, ErrClosed
	}

	msg := q.buf[0]
	// Shift the buffer. For small capacities this is acceptable.
	copy(q.buf[0:], q.buf[1:])
	q.buf = q.buf[:len(q.buf)-1]

	// Wake a blocked publisher, if any.
	q.notEmpty.Signal()

	return msg, nil
}

// Close implements Queue.Close.
func (q *InMemoryQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	q.notEmpty.Broadcast()
}

// waitWithContext waits on the condition variable while also monitoring the
// provided context for cancellation.
func (q *InMemoryQueue) waitWithContext(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		q.notEmpty.Wait()
	}()

	select {
	case <-ctx.Done():
		// Wake up goroutine blocked in Wait to avoid leaks.
		q.notEmpty.Broadcast()
		<-done
		return ctx.Err()
	case <-done:
		return nil
	}
}
