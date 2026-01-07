package mq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// HTTPQueue is an implementation of Queue that talks to a remote mq-broker
// over HTTP. It allows streamers and collectors in different pods to share
// a common in-memory queue hosted by the broker service.
type HTTPQueue struct {
	baseURL string
	client  *http.Client
}

// NewHTTPQueue constructs an HTTP-backed Queue that talks to a broker whose
// base URL should be like "http://telemetry-mq:8081".
func NewHTTPQueue(baseURL string) *HTTPQueue {
	return &HTTPQueue{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{},
	}
}

type wireMessage struct {
	ID       string            `json:"id"`
	Key      string            `json:"key"`
	Payload  string            `json:"payload"`
	Enqueued string            `json:"enqueued,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Publish sends a message to the remote broker.
func (h *HTTPQueue) Publish(ctx context.Context, msg Message) error {
	wm := wireMessage{
		ID:       msg.ID,
		Key:      msg.Key,
		Payload:  string(msg.Payload),
		Metadata: msg.Metadata,
	}
	body, err := json.Marshal(&wm)
	if err != nil {
		return fmt.Errorf("httpqueue: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/publish", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("httpqueue: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("httpqueue: publish: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusGone {
		return ErrClosed
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("httpqueue: publish unexpected status %d", resp.StatusCode)
	}
	return nil
}

// Consume blocks until a message is available on the remote broker.
func (h *HTTPQueue) Consume(ctx context.Context) (Message, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/consume", nil)
	if err != nil {
		return Message{}, fmt.Errorf("httpqueue: new request: %w", err)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return Message{}, fmt.Errorf("httpqueue: consume: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusGone {
		return Message{}, ErrClosed
	}
	if resp.StatusCode/100 != 2 {
		return Message{}, fmt.Errorf("httpqueue: consume unexpected status %d", resp.StatusCode)
	}

	var wm wireMessage
	if err := json.NewDecoder(resp.Body).Decode(&wm); err != nil {
		return Message{}, fmt.Errorf("httpqueue: decode: %w", err)
	}

	var enq time.Time
	if wm.Enqueued != "" {
		if ts, err := time.Parse(time.RFC3339Nano, wm.Enqueued); err == nil {
			enq = ts
		}
	}

	return Message{
		ID:       wm.ID,
		Key:      wm.Key,
		Payload:  []byte(wm.Payload),
		Enqueued: enq,
		Metadata: wm.Metadata,
	}, nil
}

// Close is a no-op for HTTPQueue; the remote broker's lifecycle is managed
// independently of any individual client.
func (h *HTTPQueue) Close() {}

// Ack notifies the remote broker that the message with the given ID has
// been successfully processed and can be removed from in-flight tracking.
// If the broker does not recognise the ID, the call is treated as a
// no-op.
func (h *HTTPQueue) Ack(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("httpqueue: Ack requires non-empty id")
	}

	body, err := json.Marshal(map[string]string{"id": id})
	if err != nil {
		return fmt.Errorf("httpqueue: ack marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/ack", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("httpqueue: ack new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("httpqueue: ack: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusGone {
		return ErrClosed
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("httpqueue: ack unexpected status %d", resp.StatusCode)
	}

	return nil
}


