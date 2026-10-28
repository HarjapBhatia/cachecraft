// Package repository contains the data access layer for the CacheCraft application.
// It is responsible for all direct communication with PostgreSQL.
//
// In Go, the standard library package "database/sql" provides a generic SQL
// interface. You still need a driver for the specific database you are using.
// We use "pgx/v5" (github.com/jackc/pgx/v5) as the PostgreSQL driver.
//
// DEPENDENCY: github.com/jackc/pgx/v5
//   pgx is the most performant PostgreSQL driver for Go. It has full support
//   for PostgreSQL-specific features (like LISTEN/NOTIFY and binary encoding)
//   and significantly outperforms the older "lib/pq" driver in benchmarks.
//   We register it with the standard "database/sql" interface so the rest of
//   the code stays driver-agnostic.
//
// HOW database/sql works:
//   1. sql.Open() creates a pool manager. It does NOT open actual connections.
//   2. The pool opens real TCP connections to PostgreSQL lazily, on demand.
//   3. db.SetMaxOpenConns(20) limits the pool to 20 simultaneous connections.
//   4. When all 20 are busy, the next caller blocks until one is returned.
//   5. If the caller's context.Context expires before a connection is freed,
//      the call returns context.DeadlineExceeded. We translate that to
//      domain.ErrPoolTimeout in the handler layer.
//
// HOW the artificial delay works:
//   We inject pg_sleep() directly into the SQL query. This forces the database
//   to hold one connection open for "delayMillis" milliseconds, simulating
//   a slow, real-world query. The connection stays checked-out from the pool
//   for the entire duration, which is what exhausts the pool.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"

	"cachecraft/internal/domain"

	// The blank identifier "_" imports a package solely for its side effects.
	// pgxstdlib registers the "pgx" driver name with database/sql's driver registry.
	// After this import, sql.Open("pgx", connectionString) works.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// findByIDSQL is the SQL query used to fetch a single item.
//
// Breaking it down:
//   WITH pause AS MATERIALIZED (...)
//     This is a Common Table Expression (CTE) marked as MATERIALIZED.
//     MATERIALIZED forces PostgreSQL to execute the CTE first, waiting
//     for pg_sleep to finish before the main SELECT runs. Without MATERIALIZED,
//     PostgreSQL might optimize the CTE away entirely.
//
//   SELECT pg_sleep(CAST($1 AS double precision) / 1000.0)
//     $1 is a positional parameter (Go's equivalent of Java's ? placeholder).
//     pg_sleep takes seconds, so we divide delayMillis by 1000.
//     CAST is needed because $1 comes in as an integer and pg_sleep expects float.
//
//   CROSS JOIN items AS i WHERE i.id = $2
//     This joins every row from "pause" (which is always exactly 1 row)
//     with the matching item row. The net effect is: wait, then fetch.
const findByIDSQL = `
WITH pause AS MATERIALIZED (
    SELECT pg_sleep(CAST($1 AS double precision) / 1000.0)
)
SELECT i.id, i.name, i.price, i.description
FROM pause
CROSS JOIN items AS i
WHERE i.id = $2`

// findAllSQL fetches every item ordered by ID.
// This is used by bulk-warm: we load all 10,000 rows in one query.
const findAllSQL = `
SELECT id, name, price, description
FROM items
ORDER BY id`

// PostgresItemRepo implements the service.ItemRepository interface.
// It owns the database connection pool and the query counter.
//
// In Go, struct fields starting with lowercase are "unexported" (private to
// the package). Uppercase fields are "exported" (public). This struct has
// no exported fields because all interaction happens through its methods.
type PostgresItemRepo struct {
	db         *sql.DB      // The connection pool. Shared across all goroutines.
	queryCount atomic.Int64 // Thread-safe counter for SQL statements executed.
}

// NewPostgresItemRepo creates and returns a new PostgresItemRepo.
//
// In Go, the convention for constructors is a function named "New<TypeName>"
// that returns a pointer to the newly created struct. A pointer (*T) lets you
// share and mutate the struct rather than working on a copy.
//
// Why return a pointer here?
//   atomic.Int64 must not be copied after first use. Returning a pointer
//   ensures the atomic counter is never accidentally copied.
func NewPostgresItemRepo(db *sql.DB) *PostgresItemRepo {
	return &PostgresItemRepo{db: db}
}

// FindByID fetches a single item from PostgreSQL, holding the connection open
// for delayMillis milliseconds via pg_sleep. The context carries the 2-second
// deadline set in the HTTP handler; if the pool is full and the deadline
// expires before a connection is available, this returns context.DeadlineExceeded.
//
// The caller (service layer) checks for domain.ErrItemNotFound.
// The HTTP middleware checks for context.DeadlineExceeded and maps it to 503.
func (r *PostgresItemRepo) FindByID(ctx context.Context, id int64, delayMillis int64) (domain.Item, error) {
	// QueryRowContext executes a query expected to return one row.
	// It returns *sql.Row, which holds a lazy reference to the result.
	row := r.db.QueryRowContext(ctx, findByIDSQL, delayMillis, id)
	r.queryCount.Add(1)

	var item domain.Item
	// Scan reads column values from the row into the provided pointers.
	// The order of &item.ID, &item.Name, ... must match the SELECT column order.
	err := row.Scan(&item.ID, &item.Name, &item.Price, &item.Description)
	if err != nil {
		// sql.ErrNoRows means the WHERE clause matched nothing.
		// We translate it to our domain error so the service layer and HTTP
		// handler don't need to import "database/sql".
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Item{}, domain.ErrItemNotFound
		}
		return domain.Item{}, fmt.Errorf("FindByID query failed for id=%d: %w", id, err)
	}
	return item, nil
}

// FindAll fetches every item from the table, ordered by ID.
// No artificial delay is applied here because this is only used for bulk-warm,
// not for load-test experiments.
func (r *PostgresItemRepo) FindAll(ctx context.Context) ([]domain.Item, error) {
	rows, err := r.db.QueryContext(ctx, findAllSQL)
	if err != nil {
		return nil, fmt.Errorf("FindAll query failed: %w", err)
	}
	// Always close rows when done to return the connection to the pool.
	// "defer" means: run this statement when the surrounding function returns,
	// regardless of whether it returns normally or due to an error.
	// This is Go's equivalent of Java's try-with-resources.
	defer rows.Close()

	r.queryCount.Add(1)

	var items []domain.Item
	for rows.Next() {
		var item domain.Item
		if err := rows.Scan(&item.ID, &item.Name, &item.Price, &item.Description); err != nil {
			return nil, fmt.Errorf("FindAll row scan failed: %w", err)
		}
		// "append" is Go's built-in function for adding elements to a slice.
		// A slice is Go's dynamic array (like Java ArrayList or Python list).
		items = append(items, item)
	}

	// rows.Err() returns any error that occurred during iteration.
	// Always check this after a rows loop.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("FindAll iteration error: %w", err)
	}

	return items, nil
}

// QueryCount returns the total number of SQL statements this process has run.
// atomic.Int64.Load() is a thread-safe read, safe to call from any goroutine.
func (r *PostgresItemRepo) QueryCount() int64 {
	return r.queryCount.Load()
}
