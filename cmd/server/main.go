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

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
)

func main() {
	if err := run(); err != nil {
		log.Printf("startup error: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	log.Printf("CacheCraft starting [instance=%s, env=%s, port=%s]", cfg.InstanceName, cfg.Env, cfg.Port)

	db, err := openPostgres(cfg)
	if err != nil {
		return fmt.Errorf("failed to connect to postgres: %w", err)
	}
	defer db.Close()
	log.Printf("PostgreSQL connected [maxConns=%d, timeout=%s]", cfg.DBMaxConns, cfg.DBConnTimeout)

	rdb := openRedis(cfg)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return fmt.Errorf("failed to connect to redis at %s: %w", cfg.RedisAddr, err)
	}
	defer rdb.Close()
	log.Printf("Redis connected [addr=%s]", cfg.RedisAddr)

	itemRepo := repository.NewPostgresItemRepo(db)
	itemCache := cache.NewRedisItemCache(rdb)
	sfLock := lock.NewSingleFlightLock()
	distLock := lock.NewRedisDistributedLock(rdb, cfg.LockLease)

	itemSvc := service.NewItemService(itemRepo, itemCache, sfLock, distLock)

	itemHandler := httptransport.NewItemHandler(itemSvc, cfg.DefaultTTL, cfg.DefaultJitter)
	debugHandler := httptransport.NewDebugHandler(itemRepo, itemCache, itemCache)

	router := httptransport.NewRouter(itemHandler, debugHandler, cfg.Env == "local")

	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("HTTP server listening on :%s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		return fmt.Errorf("server error: %w", err)
	case sig := <-quit:
		log.Printf("received signal %s, initiating graceful shutdown...", sig)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server forced to shutdown: %w", err)
	}

	log.Println("Server exited cleanly.")
	return nil
}

func openPostgres(cfg config.Config) (*sql.DB, error) {
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("sql.Open failed: %w", err)
	}

	db.SetMaxOpenConns(cfg.DBMaxConns)
	db.SetMaxIdleConns(cfg.DBMaxConns)
	db.SetConnMaxLifetime(0)

	pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("postgres ping failed: %w", err)
	}

	return db, nil
}

func openRedis(cfg config.Config) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		PoolSize: 100,
	})
}
