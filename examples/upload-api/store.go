package main

import (
	"context"
	_ "embed"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// openDatabase returns a lazily connecting pool.
func openDatabase(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// ParseConfig errors can echo the DSN, so they are not wrapped.
		return nil, errors.New("DATABASE_URL is not a valid connection string")
	}
	return pgxpool.NewWithConfig(ctx, config)
}
