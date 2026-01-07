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
	if err := initStreamProgress(ctx, db, streamID); err != nil {
		logger.Fatalf("failed to init stream_progress: %v", err)
	}

	// Load CSV into memory once; rows will be assigned by index from Postgres.
	file, err := os.Open(csvPath)
	if err != nil {
		logger.Fatalf("failed to open CSV: %v", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		logger.Fatalf("failed to read CSV: %v", err)
	}
	if len(records) <= 1 {
		logger.Fatalf("CSV appears to have no data rows")
	}
	// Skip header row at index 0.
	dataRows := records[1:]

	queue := mq.NewHTTPQueue(mqBaseURL)

	logger.Printf("starting streamer, CSV=%s, mq=%s, rows=%d, streamID=%s\n",
		csvPath, mqBaseURL, len(dataRows), streamID)

	// Use background context for steady-state run loop (DB already checked).
	if err := runShared(context.Background(), db, streamID, dataRows, queue, time.Duration(sleepMillis)*time.Millisecond); err != nil {
		logger.Fatalf("streamer exited: %v", err)
	}
}

// runShared loops forever, each time reserving the next row index from Postgres
// and publishing the corresponding CSV row to the MQ. All streamer pods share
// the same stream_progress row and thus have a single shared source of "next
// work item".
func runShared(ctx context.Context, db *sql.DB, streamID string, rows [][]string, queue mq.Queue, sleep time.Duration) error {
	// rows contains only data rows (header removed).
	n := int64(len(rows))
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		idx, err := nextRowIndex(ctx, db, streamID)
		if err != nil {
			return err
		}
		row := rows[idx%n]

		if len(row) < 12 {
			continue
		}

		now := time.Now().UTC()
		rec := telemetry.Record{
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
		}

		payload, err := json.Marshal(rec)
		if err != nil {
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
