package collector

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/varun6897/gpu/telemetry"
)

func TestPostgresStoreSave(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New failed: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)

	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO telemetry")).
		WithArgs(sqlmock.AnyArg(), "metric", "0", "dev", "uuid", "model", "host", "cont", "pod", "ns", "42", "labels").
		WillReturnResult(sqlmock.NewResult(1, 1))

	rec := telemetry.Record{
		Timestamp:  time.Now().UTC(),
		MetricName: "metric",
		GPUId:      "0",
		Device:     "dev",
		UUID:       "uuid",
		ModelName:  "model",
		Hostname:   "host",
		Container:  "cont",
		Pod:        "pod",
		Namespace:  "ns",
		Value:      "42",
		LabelsRaw:  "labels",
	}

	if err := store.Save(context.Background(), rec); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreListAndQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New failed: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)

	// ListGPUs
	rows := sqlmock.NewRows([]string{"uuid", "device", "model_name"}).
		AddRow("uuid0", "dev0", "model0").
		AddRow("uuid1", "dev1", "model1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DISTINCT uuid, device, model_name")).
		WillReturnRows(rows)

	gpus, err := store.ListGPUs()
	if err != nil {
		t.Fatalf("ListGPUs returned error: %v", err)
	}
	if len(gpus) != 2 || gpus[0].UUID != "uuid0" || gpus[0].Device != "dev0" || gpus[0].ModelName != "model0" {
		t.Fatalf("unexpected GPU infos: %#v", gpus)
	}

	// QueryByGPU without time window.
	ts := time.Now().UTC()
	qRows := sqlmock.NewRows([]string{
		"timestamp", "metric_name", "gpu_id", "device", "uuid", "model_name",
		"hostname", "container", "pod", "namespace", "value", "labels_raw",
	}).AddRow(ts, "metric", "0", "dev", "uuid", "model", "host", "cont", "pod", "ns", "42", "labels")

	mock.ExpectQuery(regexp.QuoteMeta("SELECT") + ".*FROM telemetry.*WHERE uuid = \\$1").
		WithArgs("uuid0").
		WillReturnRows(qRows)

	recs, err := store.QueryByGPU("uuid0", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("QueryByGPU returned error: %v", err)
	}
	if len(recs) != 1 || recs[0].GPUId != "0" {
		t.Fatalf("unexpected records: %#v", recs)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
