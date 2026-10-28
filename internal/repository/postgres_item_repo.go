package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"

	"cachecraft/internal/domain"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const findByIDSQL = `
WITH pause AS MATERIALIZED (
    SELECT pg_sleep(CAST($1 AS double precision) / 1000.0)
)
SELECT i.id, i.name, i.price, i.description
FROM pause
CROSS JOIN items AS i
WHERE i.id = $2`

const findAllSQL = `
SELECT id, name, price, description
FROM items
ORDER BY id`

// PostgresItemRepo implements service.ItemRepository using database/sql and pgx.
type PostgresItemRepo struct {
	db         *sql.DB
	queryCount atomic.Int64
}

// NewPostgresItemRepo returns a new PostgresItemRepo with the provided pool.
func NewPostgresItemRepo(db *sql.DB) *PostgresItemRepo {
	return &PostgresItemRepo{db: db}
}

// FindByID queries an item by primary key, injecting an artificial query sleep.
func (r *PostgresItemRepo) FindByID(ctx context.Context, id int64, delayMillis int64) (domain.Item, error) {
	row := r.db.QueryRowContext(ctx, findByIDSQL, delayMillis, id)
	r.queryCount.Add(1)

	var item domain.Item
	err := row.Scan(&item.ID, &item.Name, &item.Price, &item.Description)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Item{}, domain.ErrItemNotFound
		}
		return domain.Item{}, fmt.Errorf("FindByID failed for id=%d: %w", id, err)
	}
	return item, nil
}

// FindAll queries all items ordered by ID.
func (r *PostgresItemRepo) FindAll(ctx context.Context) ([]domain.Item, error) {
	rows, err := r.db.QueryContext(ctx, findAllSQL)
	if err != nil {
		return nil, fmt.Errorf("FindAll query failed: %w", err)
	}
	defer rows.Close()

	r.queryCount.Add(1)

	var items []domain.Item
	for rows.Next() {
		var item domain.Item
		if err := rows.Scan(&item.ID, &item.Name, &item.Price, &item.Description); err != nil {
			return nil, fmt.Errorf("FindAll row scan failed: %w", err)
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("FindAll iteration error: %w", err)
	}

	return items, nil
}

// QueryCount returns the number of SQL queries executed since process startup.
func (r *PostgresItemRepo) QueryCount() int64 {
	return r.queryCount.Load()
}
