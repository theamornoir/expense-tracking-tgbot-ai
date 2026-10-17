package db

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/theamornoir/expense-tracking-bot/internal/db/migrations"
)

// New connects to Postgres and applies the embedded SQL migrations.
func New(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	if err := ping(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if err := runMigrations(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return pool, nil
}

func ping(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pool.Ping(ctx); err != nil {
		return err
	}
	return nil
}

// runMigrations applies every .up.sql migration from the embedded FS in
// lexicographic order.
func runMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	entries, err := fs.ReadDir(migrations.Migrations, ".")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	// Keep only .up.sql files; slices.Sort applies them in name order.
	var upFiles []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".up.sql") {
			upFiles = append(upFiles, name)
		}
	}
	if len(upFiles) == 0 {
		return nil
	}
	slices.Sort(upFiles)

	for _, name := range upFiles {
		content, err := fs.ReadFile(migrations.Migrations, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := pool.Exec(ctx, string(content)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}

	return nil
}
