// Package testutil gives tests a clean, migrated PostgreSQL database.
// Every test package gets its own database, so packages can run in parallel without touching each other's data.
package testutil

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/db"
)

var (
	once       sync.Once
	prepared   string
	prepareErr error
)

// baseURL is the server (TEST_DATABASE_URL); the database name in it is ignored.
func baseURL() string {
	if u := os.Getenv("TEST_DATABASE_URL"); u != "" {
		return u
	}
	return "postgres://postgres@localhost:5544/postgres?sslmode=disable"
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

// URL returns the connection string of this test package's database (created and migrated on first use).
func URL() (string, error) {
	once.Do(func() {
		exe := strings.ToLower(strings.TrimSuffix(filepath.Base(os.Args[0]), filepath.Ext(os.Args[0])))
		name := "bp_" + nonWord.ReplaceAllString(exe, "_")
		if len(name) > 60 {
			name = name[:60]
		}
		u, err := url.Parse(baseURL())
		if err != nil {
			prepareErr = err
			return
		}
		ctx := context.Background()
		admin, err := pgx.Connect(ctx, u.String())
		if err != nil {
			prepareErr = err
			return
		}
		defer admin.Close(ctx)
		var exists bool
		if err := admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, name).Scan(&exists); err != nil {
			prepareErr = err
			return
		}
		if !exists {
			if _, err := admin.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize()); err != nil {
				prepareErr = err
				return
			}
		}
		u.Path = "/" + name
		prepared = u.String()
		prepareErr = db.Migrate(prepared)
	})
	return prepared, prepareErr
}

// DB returns a pool on the package's test database, with every table emptied.
func DB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	u, err := URL()
	if err != nil {
		t.Skipf("test database not available (%v); start PostgreSQL and set TEST_DATABASE_URL", err)
	}
	pool, err := db.Connect(context.Background(), u, 30)
	if err != nil {
		t.Skipf("test database not available: %v", err)
	}
	t.Cleanup(pool.Close)
	rows, err := pool.Query(context.Background(), `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('goose_db_version','combo_images')`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		names = append(names, pgx.Identifier{n}.Sanitize())
	}
	rows.Close()
	if len(names) > 0 {
		if _, err := pool.Exec(context.Background(), `TRUNCATE `+strings.Join(names, ",")+` RESTART IDENTITY CASCADE`); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}
