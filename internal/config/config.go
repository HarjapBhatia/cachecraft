// Package config is responsible for reading all application configuration from
// environment variables and providing a single typed Config struct to the rest
// of the application.
//
// WHY environment variables?
//   This follows the "12-factor app" methodology (12factor.net), which says
//   configuration that changes between deployments (dev, staging, production)
//   should come from the environment. Our docker-compose.yml sets these env
//   vars per container, just like the Java version's application.yml does.
//
// HOW does Go read env vars?
//   Go's standard library function "os.Getenv(key)" returns the value of an
//   environment variable as a string, or an empty string if it is not set.
//   We use a helper function "getEnv(key, fallback)" to provide default values.
//
// USAGE in main.go:
//   cfg, err := config.Load()
//   if err != nil { log.Fatal(err) }
//   fmt.Println(cfg.Port) // "8080"
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all runtime configuration for the CacheCraft application.
// Every field maps directly to an environment variable.
//
// In Go, a struct is like a class but with no methods attached by default.
// You can attach methods if needed, but here it is just a plain data holder.
type Config struct {
	// InstanceName identifies this specific API process in a 3-node cluster.
	// Used in logs so you can tell which node said what.
	// Env var: CACHECRAFT_INSTANCE_NAME (default: "local")
	InstanceName string

	// Port is the TCP port the HTTP server listens on inside the container.
	// Env var: PORT (default: "8080")
	Port string

	// DatabaseURL is the full PostgreSQL connection string.
	// Format: postgres://user:password@host:5432/dbname
	// Env var: DATABASE_URL (default points to local docker container)
	DatabaseURL string

	// DBMaxConns is the maximum number of open connections in the PostgreSQL
	// pool. This is the Go equivalent of HikariCP's "maximum-pool-size: 20".
	// Intentionally kept at 20 to reproduce the pool saturation experiment.
	// Env var: DB_MAX_CONNS (default: 20)
	DBMaxConns int

	// DBConnTimeout is how long a goroutine will wait for a connection from
	// the pool before giving up and returning a 503 error. This is the Go
	// equivalent of HikariCP's "connection-timeout: 2000".
	// Env var: DB_CONN_TIMEOUT_MS (default: 2000)
	DBConnTimeout time.Duration

	// RedisAddr is the Redis server address in "host:port" format.
	// Env var: REDIS_ADDR (default: "localhost:6379")
	RedisAddr string

	// DefaultTTL is the default cache expiry duration applied when the caller
	// does not specify a "ttl" query parameter.
	// Env var: DEFAULT_TTL_SECONDS (default: 30)
	DefaultTTL time.Duration

	// DefaultJitter is the default symmetric TTL jitter fraction applied when
	// the caller does not specify a "jitter" query parameter.
	// A value of 0.10 means the effective TTL is randomly adjusted by +/- 10%.
	// Env var: DEFAULT_JITTER (default: 0.10)
	DefaultJitter float64

	// LockLease is how long a distributed Redis lock lives before it expires
	// automatically, protecting against a leader crashing mid-operation.
	// Env var: LOCK_LEASE_MS (default: 5000)
	LockLease time.Duration

	// Env is the running environment. Set to "local" to enable the destructive
	// /debug/cache/reset endpoint. This mirrors Spring Boot's @Profile("local").
	// Env var: CACHECRAFT_ENV (default: "local")
	Env string
}

// Load reads all environment variables and returns a fully populated Config.
// It returns an error if any required variable fails to parse (e.g. a number
// field contains a non-numeric value).
//
// Call this once at startup in main.go and pass the Config around by value.
// Since Config is a small struct, copying it is cheap.
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

// getEnv reads an environment variable by key. If the variable is not set
// (or is set to an empty string), it returns the provided fallback value.
//
// This is a common Go helper pattern. The standard library does not have a
// built-in "get with default" for os.Getenv, so we write it ourselves.
func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
