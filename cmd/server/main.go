// Package main is the application entry point for CacheCraft.
//
// In Go, only ONE package can be named "main" and it MUST contain a "main()"
// function. This is where program execution begins, exactly like "public static
// void main(String[] args)" in Java or "if __name__ == '__main__'" in Python.
//
// This file is the "composition root" of the application:
//   1. Load configuration from environment variables.
//   2. Create infrastructure clients (PostgreSQL pool, Redis client).
//   3. Create repository, cache, and lock adapters (concrete implementations).
//   4. Wire them into the service layer.
//   5. Wire the service into the HTTP handlers.
//   6. Start the HTTP server and listen for OS shutdown signals.
//
// WHY put all wiring here?
//   This is Dependency Inversion in practice. The service layer only knows about
//   interfaces (ports.go). This main.go file is the only place that knows which
//   concrete implementations are used. Swapping Redis for Memcached would only
//   require changing this file and writing a Memcached implementation -- the
//   service, handlers, and tests would not need to change.
//
// GRACEFUL SHUTDOWN:
//   When Docker sends SIGTERM (on "docker compose down"), or you press Ctrl+C
//   (SIGINT), the application must:
//     1. Stop accepting new requests.
//     2. Wait for in-flight requests to complete.
//     3. Close the database pool and Redis connection.
//     4. Exit cleanly.
//   This is important for pool experiments: if the process exits abruptly while
//   holding connections, PostgreSQL may not release them immediately.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cachecraft/internal/cache"
	"cachecraft/internal/config"
	"cachecraft/internal/lock"
	"cachecraft/internal/repository"
	"cachecraft/internal/service"
	httptransport "cachecraft/internal/transport/http"
	"github.com/redis/go-redis/v9"

	// Blank import to register the pgx driver with database/sql.
	// See the comment in postgres_item_repo.go for why this is here.
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	// os.Exit(1) terminates the process with a non-zero code.
	// We collect all startup errors and exit if any step fails.
	if err := run(); err != nil {
		log.Printf("startup error: %v", err)
		os.Exit(1)
	}
}

// run contains the actual startup logic, separated from main() so that errors
// can be returned rather than calling os.Exit directly inside nested functions.
// This is a common Go pattern for keeping main() clean.
func run() error {
	// Step 1: Load configuration.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	log.Printf("CacheCraft starting [instance=%s, env=%s, port=%s]", cfg.InstanceName, cfg.Env, cfg.Port)

	// Step 2: Connect to PostgreSQL.
	db, err := openPostgres(cfg)
	if err != nil {
		return fmt.Errorf("failed to connect to postgres: %w", err)
	}
	defer db.Close() // Close the pool when main() exits.
	log.Printf("PostgreSQL connected [maxConns=%d, timeout=%s]", cfg.DBMaxConns, cfg.DBConnTimeout)

	// Step 3: Connect to Redis.
	rdb := openRedis(cfg)
	// Ping verifies the connection is live. Fail fast if Redis is unreachable.
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return fmt.Errorf("failed to connect to redis at %s: %w", cfg.RedisAddr, err)
	}
	defer rdb.Close()
	log.Printf("Redis connected [addr=%s]", cfg.RedisAddr)

	// Step 4: Wire up the infrastructure adapters.
	// These are the concrete implementations that satisfy the service interfaces.
	itemRepo := repository.NewPostgresItemRepo(db)
	itemCache := cache.NewRedisItemCache(rdb)
	sfLock := lock.NewSingleFlightLock()
	distLock := lock.NewRedisDistributedLock(rdb, cfg.LockLease)

	// Step 5: Wire up the service layer.
	// ItemService only knows about the interface types, not the concrete structs.
	// Go satisfies the interfaces implicitly -- no "implements" keyword needed.
	itemSvc := service.NewItemService(itemRepo, itemCache, sfLock, distLock)

	// Step 6: Wire up the HTTP handlers.
	itemHandler := httptransport.NewItemHandler(itemSvc, cfg.DefaultTTL, cfg.DefaultJitter)
	debugHandler := httptransport.NewDebugHandler(itemRepo, itemCache, itemCache)

	// Step 7: Build the router.
	isLocal := cfg.Env == "local"
	router := httptransport.NewRouter(itemHandler, debugHandler, isLocal)

	// Step 8: Configure and start the HTTP server.
	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: router,
		// ReadTimeout is how long the server waits for the full request to arrive.
		// WriteTimeout is how long it waits for the handler to write the response.
		// These prevent slow clients from holding connections open indefinitely.
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Run the server in a goroutine so we can listen for shutdown signals in
	// the main goroutine concurrently.
	//
	// "go func() { ... }()" is an immediately-invoked goroutine.
	// The "()" at the end calls the anonymous function immediately.
	serverErr := make(chan error, 1) // buffered channel: holds 1 error without blocking
	go func() {
		log.Printf("HTTP server listening on :%s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// Step 9: Listen for OS signals for graceful shutdown.
	// make(chan os.Signal, 1) creates a channel that holds OS signals.
	// signal.Notify registers the channel to receive SIGINT (Ctrl+C) and SIGTERM (Docker stop).
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// "select" blocks until one of its cases is ready.
	// This is like a switch statement for channels: whichever channel has data first wins.
	select {
	case err := <-serverErr:
		return fmt.Errorf("server error: %w", err)
	case sig := <-quit:
		log.Printf("received signal %s, shutting down...", sig)
	}

	// Step 10: Graceful shutdown.
	// Give in-flight requests up to 30 seconds to complete before force-closing.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server forced to shutdown: %w", err)
	}

	log.Println("Server exited cleanly.")
	return nil
}

// openPostgres creates and configures the database/sql connection pool.
//
// Important: sql.Open does NOT open actual TCP connections.
// It creates the pool manager. Real connections are opened lazily on demand.
// db.Ping() is the first call that actually opens a connection.
func openPostgres(cfg config.Config) (*sql.DB, error) {
	// "pgx" is the driver name registered by the blank import of pgx/stdlib.
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("sql.Open failed: %w", err)
	}

	// This is the key setting for the saturation experiment.
	// It limits the pool to exactly 20 connections, mirroring HikariCP.
	db.SetMaxOpenConns(cfg.DBMaxConns)

	// Keep up to DBMaxConns idle connections ready. Without this, the pool might
	// close connections between requests, causing reconnection overhead.
	db.SetMaxIdleConns(cfg.DBMaxConns)

	// Connections older than this are gracefully recycled. 0 means they live forever.
	db.SetConnMaxLifetime(0)

	// Ping opens one real connection to verify PostgreSQL is reachable.
	// context.Background() is a never-cancelled, never-timed-out context.
	// We use it here because we want startup to wait indefinitely (Docker
	// health checks ensure Postgres is ready before our container starts).
	pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("postgres ping failed: %w", err)
	}

	return db, nil
}

// openRedis creates a go-redis client connected to the configured Redis address.
// go-redis also manages an internal connection pool.
func openRedis(cfg config.Config) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr: cfg.RedisAddr,
		// PoolSize is the max number of socket connections in the pool.
		// Default is 10. For our experiment, the HTTP concurrency is high,
		// so we set it to match the maximum goroutine concurrency expected.
		PoolSize: 100,
	})
}
