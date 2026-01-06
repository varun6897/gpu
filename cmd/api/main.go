package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/varunv/gpu/api"
	"github.com/varunv/gpu/collector"
)

func main() {
	logger := log.New(os.Stdout, "[api] ", log.LstdFlags|log.Lmicroseconds|log.LUTC)

	postgresDSN := getenv("POSTGRES_DSN", "")
	if postgresDSN == "" {
		logger.Fatal("POSTGRES_DSN is required")
	}

	listenAddr := getenv("LISTEN_ADDR", ":8080")

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
	srv := api.NewServer(store)

	logger.Printf("starting HTTP API on %s\n", listenAddr)
	if err := http.ListenAndServe(listenAddr, srv); err != nil {
		logger.Fatalf("http server exited: %v", err)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}


