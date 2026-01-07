package collector

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/varun6897/gpu/telemetry"
)

// PostgresStore is a Store implementation backed by a PostgreSQL database.
// It assumes a table with the following schema (field names can be adjusted
// as needed, but types should be compatible):
//
//	CREATE TABLE IF NOT EXISTS telemetry (
//	  id          BIGSERIAL PRIMARY KEY,
//	  timestamp   TIMESTAMPTZ NOT NULL,
//	  metric_name TEXT        NOT NULL,
//	  gpu_id      TEXT        NOT NULL,
//	  device      TEXT        NOT NULL,
//	  uuid        TEXT        NOT NULL,
//	  model_name  TEXT        NOT NULL,
//	  hostname    TEXT        NOT NULL,
//	  container   TEXT        NOT NULL,
//	  pod         TEXT        NOT NULL,
//	  namespace   TEXT        NOT NULL,
//	  value       TEXT        NOT NULL,
//	  labels_raw  TEXT        NOT NULL
//	);
//
// All collector instances in the cluster can safely share the same database.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore wraps an existing *sql.DB and returns a Store implementation
// suitable for use by collectors and the API layer.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// Save inserts a telemetry record into the telemetry table.
func (s *PostgresStore) Save(ctx context.Context, rec telemetry.Record) error {
	const insertStmt = `
INSERT INTO telemetry (
  timestamp, metric_name, gpu_id, device, uuid, model_name,
  hostname, container, pod, namespace, value, labels_raw
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
`
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, insertStmt,
		rec.Timestamp,
		rec.MetricName,
		rec.GPUId,
		rec.Device,
		rec.UUID,
		rec.ModelName,
		rec.Hostname,
		rec.Container,
		rec.Pod,
		rec.Namespace,
		rec.Value,
		rec.LabelsRaw,
	)
	if err != nil {
		return fmt.Errorf("postgres store: insert telemetry: %w", err)
	}
	return nil
}

// ListGPUs returns distinct GPU metadata (uuid, device, modelName).
func (s *PostgresStore) ListGPUs() ([]telemetry.GPUInfo, error) {
	const q = `
SELECT DISTINCT uuid, device, model_name
FROM telemetry
`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list gpus: %w", err)
	}
	defer rows.Close()

	var infos []telemetry.GPUInfo
	for rows.Next() {
		var info telemetry.GPUInfo
		if err := rows.Scan(&info.UUID, &info.Device, &info.ModelName); err != nil {
			return nil, fmt.Errorf("postgres store: scan gpu info: %w", err)
		}
		infos = append(infos, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: rows error: %w", err)
	}
	return infos, nil
}

// QueryByGPU returns telemetry for a GPU UUID, optionally constrained by a time
// window. Results are ordered by timestamp descending (latest first).
func (s *PostgresStore) QueryByGPU(uuid string, start, end time.Time) ([]telemetry.Record, error) {
	base := `
SELECT
  timestamp, metric_name, gpu_id, device, uuid, model_name,
  hostname, container, pod, namespace, value, labels_raw
FROM telemetry
WHERE uuid = $1
`
	args := []any{uuid}
	argIdx := 2

	if !start.IsZero() {
		base += fmt.Sprintf(" AND timestamp >= $%d", argIdx)
		args = append(args, start)
		argIdx++
	}
	if !end.IsZero() {
		base += fmt.Sprintf(" AND timestamp <= $%d", argIdx)
		args = append(args, end)
		argIdx++
	}

	base += " ORDER BY timestamp DESC"

	rows, err := s.db.Query(base, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: query by gpu: %w", err)
	}
	defer rows.Close()

	var out []telemetry.Record
	for rows.Next() {
		var rec telemetry.Record
		if err := rows.Scan(
			&rec.Timestamp,
			&rec.MetricName,
			&rec.GPUId,
			&rec.Device,
			&rec.UUID,
			&rec.ModelName,
			&rec.Hostname,
			&rec.Container,
			&rec.Pod,
			&rec.Namespace,
			&rec.Value,
			&rec.LabelsRaw,
		); err != nil {
			return nil, fmt.Errorf("postgres store: scan record: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: rows error: %w", err)
	}
	return out, nil
}
