package streamer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/varunv/gpu/mq"
)

func TestRunStreamsRecordsOnce(t *testing.T) {
	tmpDir := t.TempDir()
	csvPath := filepath.Join(tmpDir, "test.csv")

	const csvContent = `timestamp,metric_name,gpu_id,device,uuid,modelName,Hostname,container,pod,namespace,value,labels_raw
"2025-07-18T20:42:34Z","DCGM_FI_DEV_GPU_UTIL","0","nvidia0","GPU-123","NVIDIA H100 80GB HBM3","host","","","","42","labels"
`

	if err := os.WriteFile(csvPath, []byte(csvContent), 0o644); err != nil {
		t.Fatalf("failed to write temp CSV: %v", err)
	}

	q := mq.NewInMemoryQueue(10)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		if err := Run(ctx, Config{
			CSVPath: csvPath,
			Queue:   q,
			Loop:    false,
		}); err != nil {
			// Fail the test from goroutine is hard; log instead.
			t.Logf("Run returned error: %v", err)
		}
	}()

	msg, err := q.Consume(ctx)
	if err != nil {
		t.Fatalf("Consume failed: %v", err)
	}

	if msg.Key != "0" {
		t.Fatalf("expected Key=gpu_id '0', got %q", msg.Key)
	}
	if len(msg.Payload) == 0 {
		t.Fatalf("expected non-empty payload")
	}
}

func TestShardingDistributesRecordsWithoutDuplication(t *testing.T) {
	tmpDir := t.TempDir()
	csvPath := filepath.Join(tmpDir, "test_sharded.csv")

	const csvContent = `timestamp,metric_name,gpu_id,device,uuid,modelName,Hostname,container,pod,namespace,value,labels_raw
"2025-07-18T20:42:34Z","DCGM_FI_DEV_GPU_UTIL","0","nvidia0","GPU-0","NVIDIA","host","","","","1","labels"
"2025-07-18T20:42:34Z","DCGM_FI_DEV_GPU_UTIL","1","nvidia1","GPU-1","NVIDIA","host","","","","2","labels"
"2025-07-18T20:42:34Z","DCGM_FI_DEV_GPU_UTIL","2","nvidia2","GPU-2","NVIDIA","host","","","","3","labels"
`

	if err := os.WriteFile(csvPath, []byte(csvContent), 0o644); err != nil {
		t.Fatalf("failed to write temp CSV: %v", err)
	}

	q := mq.NewInMemoryQueue(10)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Two shards cooperating over the same CSV.
	for shard := 0; shard < 2; shard++ {
		shard := shard
		go func() {
			if err := Run(ctx, Config{
				CSVPath: csvPath,
				Queue:   q,
				Loop:    false,
				// enable sharding
				ShardCount: 2,
				ShardIndex: shard,
			}); err != nil {
				t.Logf("Run (shard %d) returned error: %v", shard, err)
			}
		}()
	}

	// We expect exactly 3 messages total, one per GPU ID, with no duplicates.
	seen := make(map[string]bool)
	for len(seen) < 3 {
		msg, err := q.Consume(ctx)
		if err != nil {
			t.Fatalf("Consume failed while waiting for sharded messages: %v", err)
		}
		if seen[msg.Key] {
			t.Fatalf("duplicate gpu_id/key %q received from different shards", msg.Key)
		}
		seen[msg.Key] = true
	}
}

func TestRewindCSV(t *testing.T) {
	tmpDir := t.TempDir()
	csvPath := filepath.Join(tmpDir, "test_rewind.csv")

	const csvContent = `timestamp,metric_name,gpu_id,device,uuid,modelName,Hostname,container,pod,namespace,value,labels_raw
"2025-07-18T20:42:34Z","DCGM_FI_DEV_GPU_UTIL","0","nvidia0","GPU-0","NVIDIA","host","","","","1","labels"
`
	if err := os.WriteFile(csvPath, []byte(csvContent), 0o644); err != nil {
		t.Fatalf("failed to write temp CSV: %v", err)
	}

	f, err := os.Open(csvPath)
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	defer f.Close()

	r := newCSVReader(f)
	if _, err := r.Read(); err != nil { // header
		t.Fatalf("failed to read header: %v", err)
	}

	if err := rewindCSV(f, &r); err != nil {
		t.Fatalf("rewindCSV failed: %v", err)
	}

	// After rewind, we should again be able to read the header first.
	if _, err := r.Read(); err != nil {
		t.Fatalf("failed to read header after rewind: %v", err)
	}
}

type fakeQueue struct {
	published []mq.Message
	err       error
}

func (f *fakeQueue) Publish(_ context.Context, msg mq.Message) error {
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, msg)
	return nil
}

func (f *fakeQueue) Consume(context.Context) (mq.Message, error) {
	return mq.Message{}, nil
}

func (f *fakeQueue) Close() {}

// TestRunShardSkip verifies that when sharding excludes a record, it is not
// published to the queue.
func TestRunShardSkip(t *testing.T) {
	tmpDir := t.TempDir()
	csvPath := filepath.Join(tmpDir, "shard.csv")

	const csvContent = `timestamp,metric_name,gpu_id,device,uuid,modelName,Hostname,container,pod,namespace,value,labels_raw
"2025-07-18T20:42:34Z","DCGM_FI_DEV_GPU_UTIL","0","nvidia0","GPU-0","NVIDIA","host","","","","1","labels"
`
	if err := os.WriteFile(csvPath, []byte(csvContent), 0o644); err != nil {
		t.Fatalf("failed to write temp CSV: %v", err)
	}

	fq := &fakeQueue{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Determine which shard index owns gpu_id "0" and pick the other one.
	var owner int
	for i := 0; i < 2; i++ {
		if ownsRecord("0", i, 2) {
			owner = i
			break
		}
	}
	nonOwner := 1 - owner

	cfg := Config{
		CSVPath:    csvPath,
		Queue:      fq,
		Loop:       false,
		ShardCount: 2,
		ShardIndex: nonOwner,
	}

	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(fq.published) != 0 {
		t.Fatalf("expected no messages published for non-owning shard, got %d", len(fq.published))
	}
}

// TestRunPublishError ensures that an error from Queue.Publish is propagated.
func TestRunPublishError(t *testing.T) {
	tmpDir := t.TempDir()
	csvPath := filepath.Join(tmpDir, "error.csv")

	const csvContent = `timestamp,metric_name,gpu_id,device,uuid,modelName,Hostname,container,pod,namespace,value,labels_raw
"2025-07-18T20:42:34Z","DCGM_FI_DEV_GPU_UTIL","0","nvidia0","GPU-0","NVIDIA","host","","","","1","labels"
`
	if err := os.WriteFile(csvPath, []byte(csvContent), 0o644); err != nil {
		t.Fatalf("failed to write temp CSV: %v", err)
	}

	fq := &fakeQueue{err: mq.ErrClosed}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	cfg := Config{
		CSVPath: csvPath,
		Queue:   fq,
		Loop:    false,
	}

	if err := Run(ctx, cfg); err == nil {
		t.Fatalf("expected error from Run when Publish fails")
	}
}

// TestRunImmediateCancel hits the context cancellation path at the top of the loop.
func TestRunImmediateCancel(t *testing.T) {
	tmpDir := t.TempDir()
	csvPath := filepath.Join(tmpDir, "cancel.csv")

	const csvContent = `timestamp,metric_name,gpu_id,device,uuid,modelName,Hostname,container,pod,namespace,value,labels_raw
"2025-07-18T20:42:34Z","DCGM_FI_DEV_GPU_UTIL","0","nvidia0","GPU-0","NVIDIA","host","","","","1","labels"
`
	if err := os.WriteFile(csvPath, []byte(csvContent), 0o644); err != nil {
		t.Fatalf("failed to write temp CSV: %v", err)
	}

	fq := &fakeQueue{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Run starts

	cfg := Config{
		CSVPath: csvPath,
		Queue:   fq,
		Loop:    false,
	}

	if err := Run(ctx, cfg); err == nil {
		t.Fatalf("expected context cancellation error from Run")
	}
}
