package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/varun6897/gpu/mq"
	"github.com/varun6897/gpu/telemetry"
)

// RecordSource produces telemetry records from some underlying source.
// Implementations decide how work is coordinated (CSV row indices, exporters,
// Kafka partitions, etc.). The streamer loop only cares about this interface.
type RecordSource interface {
	// Next returns the next telemetry record, or an error. It should honour
	// ctx for cancellation and may block while waiting for new data.
	Next(ctx context.Context) (telemetry.Record, error)
}

func main() {
	logger := log.New(os.Stdout, "[streamer] ", log.LstdFlags|log.Lmicroseconds|log.LUTC)

	csvPath := getenv("CSV_PATH", "/data/dcgm_metrics_20250718_134233.csv")
	mqBaseURL := getenv("MQ_BASE_URL", "http://telemetry-mq:8081")
	sleepMillis := getEnvInt("STREAMER_SLEEP_MILLIS", 100)
	postgresDSN := getenv("POSTGRES_DSN", "")
	streamID := getenv("STREAM_ID", "dcgm_csv")

	if postgresDSN == "" {
		logger.Fatal("POSTGRES_DSN is required for shared work coordination")
	}

	ctx := context.Background()

	db, err := sql.Open("pgx", postgresDSN)
	if err != nil {
		logger.Fatalf("failed to open postgres connection: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		logger.Fatalf("failed to ping postgres: %v", err)
	}

	// For now we only have a CSV+Postgres-backed RecordSource that uses
	// stream_progress to coordinate work across pods. In the future, additional
	// sources (e.g. exporters) can be plugged in by implementing RecordSource.
	src, err := NewCSVIndexSource(ctx, db, streamID, csvPath)
	if err != nil {
		logger.Fatalf("failed to create CSV source: %v", err)
	}

	queue := mq.NewHTTPQueue(mqBaseURL)

	logger.Printf("starting streamer, CSV=%s, mq=%s, streamID=%s\n",
		csvPath, mqBaseURL, streamID)

	// Use background context for steady-state run loop (DB already checked).
	if err := RunStreamer(context.Background(), src, queue, time.Duration(sleepMillis)*time.Millisecond); err != nil {
		logger.Fatalf("streamer exited: %v", err)
	}
}

// RunStreamer is the generic publish loop. It pulls telemetry records from the
// provided RecordSource and publishes them to the MQ. This loop is agnostic to
// where records come from (CSV, exporters, Kafka, etc.).
func RunStreamer(ctx context.Context, src RecordSource, queue mq.Queue, sleep time.Duration) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		rec, err := src.Next(ctx)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		if rec.Timestamp.IsZero() {
			rec.Timestamp = now
		}

		payload, err := json.Marshal(rec)
		if err != nil {
			// Skip this record; JSON errors are unlikely but non-fatal.
			continue
		}

		msg := mq.Message{
			ID:      fmt.Sprintf("%s|%s|%s|%d", rec.UUID, rec.GPUId, rec.MetricName, now.UnixNano()),
			Key:     rec.GPUId,
			Payload: payload,
			Metadata: map[string]string{
				"metric_name": rec.MetricName,
				"hostname":    rec.Hostname,
				"uuid":        rec.UUID,
			},
		}

		if err := queue.Publish(ctx, msg); err != nil {
			return err
		}

		if sleep > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(sleep):
			}
		}
	}
}

// CSVIndexSource is a RecordSource implementation that:
//   - Loads all CSV rows into memory once (minus the header).
//   - Uses the shared Postgres stream_progress table to reserve a unique row
//     index across all streamer pods.
//   - Maps the reserved index into the in-memory rows slice (with modulo) to
//     produce a telemetry.Record.
type CSVIndexSource struct {
	db       *sql.DB
	streamID string
	rows     [][]string // data rows, header already stripped
}

// NewCSVIndexSource constructs a CSVIndexSource by initialising the
// stream_progress table (if needed) and loading the CSV file into memory.
func NewCSVIndexSource(ctx context.Context, db *sql.DB, streamID, csvPath string) (*CSVIndexSource, error) {
	if err := initStreamProgress(ctx, db, streamID); err != nil {
		return nil, err
	}

	file, err := os.Open(csvPath)
	if err != nil {
		return nil, fmt.Errorf("open CSV: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read CSV: %w", err)
	}
	if len(records) <= 1 {
		return nil, fmt.Errorf("CSV appears to have no data rows")
	}

	// Strip header row at index 0.
	dataRows := records[1:]

	return &CSVIndexSource{
		db:       db,
		streamID: streamID,
		rows:     dataRows,
	}, nil
}

// Next reserves the next row index from Postgres and returns the corresponding
// telemetry.Record. All streamer pods that share the same streamID and CSV
// cooperate via the shared stream_progress row.
func (s *CSVIndexSource) Next(ctx context.Context) (telemetry.Record, error) {
	n := int64(len(s.rows))

	idx, err := nextRowIndex(ctx, s.db, s.streamID)
	if err != nil {
		return telemetry.Record{}, err
	}
	row := s.rows[idx%n]

	if len(row) < 12 {
		// Skip malformed rows rather than failing the whole streamer.
		return s.Next(ctx)
	}

	now := time.Now().UTC()
	return telemetry.Record{
		Timestamp:  now,
		MetricName: row[1],
		GPUId:      row[2],
		Device:     row[3],
		UUID:       row[4],
		ModelName:  row[5],
		Hostname:   row[6],
		Container:  row[7],
		Pod:        row[8],
		Namespace:  row[9],
		Value:      row[10],
		LabelsRaw:  row[11],
	}, nil
}

func initStreamProgress(ctx context.Context, db *sql.DB, streamID string) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS stream_progress (
  stream_id TEXT PRIMARY KEY,
  next_row  BIGINT NOT NULL
);`
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create stream_progress: %w", err)
	}

	const ins = `
INSERT INTO stream_progress(stream_id, next_row)
VALUES ($1, 0)
ON CONFLICT (stream_id) DO NOTHING;
`
	if _, err := db.ExecContext(ctx, ins, streamID); err != nil {
		return fmt.Errorf("init stream_progress row: %w", err)
	}
	return nil
}

func nextRowIndex(ctx context.Context, db *sql.DB, streamID string) (int64, error) {
	const q = `
INSERT INTO stream_progress(stream_id, next_row)
VALUES ($1, 1)
ON CONFLICT (stream_id)
DO UPDATE SET next_row = stream_progress.next_row + 1
RETURNING next_row - 1;
`
	var idx int64
	if err := db.QueryRowContext(ctx, q, streamID).Scan(&idx); err != nil {
		return 0, fmt.Errorf("reserve next_row: %w", err)
	}
	return idx, nil
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
