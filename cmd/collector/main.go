package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/varunv/gpu/collector"
	"github.com/varunv/gpu/mq"
)

func main() {
	logger := log.New(os.Stdout, "[collector] ", log.LstdFlags|log.Lmicroseconds|log.LUTC)

	postgresDSN := getenv("POSTGRES_DSN", "")
	if postgresDSN == "" {
		logger.Fatal("POSTGRES_DSN is required")
	}
	mqBaseURL := getenv("MQ_BASE_URL", "http://telemetry-mq:8081")
	workers := getEnvInt("COLLECTOR_WORKERS", 4)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := sql.Open("pgx", postgresDSN)
	if err != nil {
		logger.Fatalf("failed to open postgres connection: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		logger.Fatalf("failed to ping postgres: %v", err)
	}
	logger.Println("connected to postgres")

	store := collector.NewPostgresStore(db)
	queue := mq.NewHTTPQueue(mqBaseURL)

	logger.Printf("starting collector with %d workers, mq=%s\n", workers, mqBaseURL)

	// Use a background context for run loop; DB connectivity already checked.
	if err := collector.Run(context.Background(), collector.Config{
		Queue:   queue,
		Store:   store,
		Workers: workers,
	}); err != nil {
		logger.Fatalf("collector exited: %v", err)
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


