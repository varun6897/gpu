package streamer

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"time"

	"github.com/varun6897/gpu/mq"
	"github.com/varun6897/gpu/telemetry"
)

// Config controls how the telemetry streamer behaves.
type Config struct {
	// CSVPath is the path to the DCGM CSV file.
	CSVPath string
	// Queue is the destination message queue.
	Queue mq.Queue
	// Loop controls whether the streamer should loop over the CSV
	// indefinitely. When false, the streamer stops after one pass.
	Loop bool
	// SleepBetweenRecords is an optional delay between records to
	// simulate real-time streaming load. Zero means no delay.
	SleepBetweenRecords time.Duration

	// ShardCount and ShardIndex allow multiple streamer instances (e.g. pods)
	// to cooperatively consume from the SAME CSV without duplicating logical
	// telemetry records.
	//
	// Sharding is performed by hashing the gpu_id column:
	//   shard = hash(gpu_id) % ShardCount
	//   record is published only by the streamer with ShardIndex == shard.
	//
	// This means that up to ShardCount independent streamer processes, all
	// configured with the same CSVPath and ShardCount but different ShardIndex
	// values in [0, ShardCount), can safely run in parallel without emitting
	// duplicate telemetry for the same CSV row.
	//
	// If ShardCount <= 0, sharding is disabled and all records are published.
	ShardCount int
	ShardIndex int
}

// Run starts a telemetry streamer that reads the DCGM CSV and publishes
// each row as a TelemetryRecord message to the provided queue.
//
// Behaviour:
//   - The CSV header is skipped.
//   - For each row, the "timestamp" column from the file is ignored and
//     replaced with the current time (UTC) as required by the design doc
//     in GPU Telemetry Pipeline Message Queue.pdf.
//   - Each row is encoded as JSON and sent as mq.Message.Payload.
//   - mq.Message.Key is set to the gpu_id so collectors can shard by GPU.
//   - When the end of the file is reached:
//   - If cfg.Loop is true, the streamer rewinds to the start (after header)
//     and continues.
//   - If cfg.Loop is false, Run returns nil.
//   - The context can be used to stop the streamer at any time.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Queue == nil {
		return fmt.Errorf("streamer: Queue is required")
	}
	if cfg.CSVPath == "" {
		return fmt.Errorf("streamer: CSVPath is required")
	}

	f, err := os.Open(cfg.CSVPath)
	if err != nil {
		return fmt.Errorf("streamer: open CSV: %w", err)
	}
	defer f.Close()

	if cfg.ShardCount < 0 {
		return fmt.Errorf("streamer: ShardCount must be >= 0")
	}
	if cfg.ShardCount > 0 && (cfg.ShardIndex < 0 || cfg.ShardIndex >= cfg.ShardCount) {
		return fmt.Errorf("streamer: ShardIndex %d out of range [0,%d)", cfg.ShardIndex, cfg.ShardCount)
	}

	reader := newCSVReader(f)
	// Skip header.
	if _, err := reader.Read(); err != nil {
		return fmt.Errorf("streamer: read header: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		record, err := reader.Read()
		if err == io.EOF {
			if !cfg.Loop {
				return nil
			}
			// Rewind and continue.
			if err := rewindCSV(f, &reader); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("streamer: read row: %w", err)
		}

		// Expect 12 columns as per sample file.
		if len(record) < 12 {
			// Skip malformed rows rather than failing the whole streamer.
			continue
		}

		now := time.Now().UTC()

		t := telemetry.Record{
			Timestamp:  now,
			MetricName: record[1],
			GPUId:      record[2],
			Device:     record[3],
			UUID:       record[4],
			ModelName:  record[5],
			Hostname:   record[6],
			Container:  record[7],
			Pod:        record[8],
			Namespace:  record[9],
			Value:      record[10],
			LabelsRaw:  record[11],
		}

		// If sharding is enabled, only publish records owned by this shard.
		if cfg.ShardCount > 0 && !ownsRecord(t.GPUId, cfg.ShardIndex, cfg.ShardCount) {
			// Not this shard's responsibility; skip.
			continue
		}

		payload, err := json.Marshal(t)
		if err != nil {
			// Skip this record; JSON errors are unlikely but non-fatal.
			continue
		}

		msg := mq.Message{
			ID:      fmt.Sprintf("%s|%s|%s|%d", t.UUID, t.GPUId, t.MetricName, now.UnixNano()),
			Key:     t.GPUId,
			Payload: payload,
			// Let the queue set Enqueued if needed.
			Metadata: map[string]string{
				"metric_name": t.MetricName,
				"hostname":    t.Hostname,
				"uuid":        t.UUID,
			},
		}

		if err := cfg.Queue.Publish(ctx, msg); err != nil {
			// If the queue is closed or context cancelled, propagate error.
			return fmt.Errorf("streamer: publish: %w", err)
		}

		if cfg.SleepBetweenRecords > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(cfg.SleepBetweenRecords):
			}
		}
	}
}

func newCSVReader(r io.Reader) *csv.Reader {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // be tolerant of variable columns
	return cr
}

// rewindCSV seeks the file back to the beginning and skips the header,
// returning a fresh csv.Reader.
func rewindCSV(f *os.File, reader **csv.Reader) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("streamer: rewind CSV: %w", err)
	}
	r := newCSVReader(f)
	if _, err := r.Read(); err != nil {
		return fmt.Errorf("streamer: read header after rewind: %w", err)
	}
	*reader = r
	return nil
}

// ownsRecord determines whether the given gpuID should be handled by the shard
// identified by (shardIndex, shardCount).
//
// When shardCount <= 0, sharding is considered disabled and the function
// always returns true.
func ownsRecord(gpuID string, shardIndex, shardCount int) bool {
	if shardCount <= 0 {
		return true
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(gpuID))
	return int(h.Sum32()%uint32(shardCount)) == shardIndex
}
