package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds runtime configuration loaded from environment variables.
type Config struct {
	InstanceName  string
	Port          string
	DatabaseURL   string
	DBMaxConns    int
	DBConnTimeout time.Duration
	RedisAddr     string
	DefaultTTL    time.Duration
	DefaultJitter float64
	LockLease     time.Duration
	Env           string
}

// Load parses environment variables into a typed Config struct with sane defaults.
func Load() (Config, error) {
	dbMaxConns, err := strconv.Atoi(getEnv("DB_MAX_CONNS", "20"))
	if err != nil {
		return Config{}, fmt.Errorf("DB_MAX_CONNS must be an integer: %w", err)
	}

	dbTimeoutMs, err := strconv.Atoi(getEnv("DB_CONN_TIMEOUT_MS", "2000"))
	if err != nil {
		return Config{}, fmt.Errorf("DB_CONN_TIMEOUT_MS must be an integer: %w", err)
	}

	defaultTTLSec, err := strconv.Atoi(getEnv("DEFAULT_TTL_SECONDS", "30"))
	if err != nil {
		return Config{}, fmt.Errorf("DEFAULT_TTL_SECONDS must be an integer: %w", err)
	}

	defaultJitter, err := strconv.ParseFloat(getEnv("DEFAULT_JITTER", "0.10"), 64)
	if err != nil {
		return Config{}, fmt.Errorf("DEFAULT_JITTER must be a float: %w", err)
	}

	lockLeaseMs, err := strconv.Atoi(getEnv("LOCK_LEASE_MS", "5000"))
	if err != nil {
		return Config{}, fmt.Errorf("LOCK_LEASE_MS must be an integer: %w", err)
	}

	return Config{
		InstanceName:  getEnv("CACHECRAFT_INSTANCE_NAME", "local"),
		Port:          getEnv("PORT", "8080"),
		DatabaseURL:   getEnv("DATABASE_URL", "postgres://cachecraft:cachecraft@localhost:5432/cachecraft?sslmode=disable"),
		DBMaxConns:    dbMaxConns,
		DBConnTimeout: time.Duration(dbTimeoutMs) * time.Millisecond,
		RedisAddr:     getEnv("REDIS_ADDR", "localhost:6379"),
		DefaultTTL:    time.Duration(defaultTTLSec) * time.Second,
		DefaultJitter: defaultJitter,
		LockLease:     time.Duration(lockLeaseMs) * time.Millisecond,
		Env:           getEnv("CACHECRAFT_ENV", "local"),
	}, nil
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
