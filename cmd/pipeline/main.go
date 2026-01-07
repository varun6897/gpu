package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/varun6897/gpu/collector"
	"github.com/varun6897/gpu/mq"
	"github.com/varun6897/gpu/streamer"
)

func main() {
	logger := log.New(os.Stdout, "[pipeline] ", log.LstdFlags|log.Lmicroseconds|log.LUTC)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	postgresDSN := getenv("POSTGRES_DSN", "")
	if postgresDSN == "" {
		logger.Fatal("POSTGRES_DSN is required")
	}

	csvPath := getenv("CSV_PATH", "/data/dcgm_metrics_20250718_134233.csv")
	queueCapacity := getEnvInt("QUEUE_CAPACITY", 1000)
	collectorWorkers := getEnvInt("COLLECTOR_WORKERS", 4)
	sleepMillis := getEnvInt("STREAMER_SLEEP_MILLIS", 100)

	db, err := sql.Open("pgx", postgresDSN)
	if err != nil {
		logger.Fatalf("failed to open postgres connection: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		logger.Fatalf("failed to ping postgres: %v", err)
	}
	logger.Println("connected to postgres")

	store := collector.NewPostgresStore(db)
	queue := mq.NewInMemoryQueue(queueCapacity)

	// Start the collector in the background.
	go func() {
		logger.Printf("starting collector with %d workers\n", collectorWorkers)
		if err := collector.Run(ctx, collector.Config{
			Queue:   queue,
			Store:   store,
			Workers: collectorWorkers,
		}); err != nil {
			logger.Printf("collector exited with error: %v\n", err)
			cancel()
		}
	}()

	// Run the streamer in the foreground.
	logger.Printf("starting streamer reading from %s\n", csvPath)
	if err := streamer.Run(ctx, streamer.Config{
		CSVPath:             csvPath,
		Queue:               queue,
		Loop:                true,
		SleepBetweenRecords: time.Duration(sleepMillis) * time.Millisecond,
	}); err != nil {
		logger.Fatalf("streamer exited with error: %v", err)
	}
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
