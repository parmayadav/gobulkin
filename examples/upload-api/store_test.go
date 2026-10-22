package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/parmayadav/gobulkin/pkg/loader"
)

// Runs against a real database; set TEST_DATABASE_URL to enable. Everything happens in a throwaway schema.
func TestCustomerLoader_Integration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close(ctx)

	schema := fmt.Sprintf("gobulkin_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.RuntimeParams["search_path"] = schema
	if _, err := admin.Exec(ctx, "SET search_path TO "+schema+"; "+schemaSQL); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	customers, err := newCustomerLoader()
	if err != nil {
		t.Fatalf("newCustomerLoader: %v", err)
	}

	// Like a handler: begin, import, then commit or roll back based on the report.
	upload := func(csv string, commitPartial bool) (*loader.Report, error) {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		report, err := customers.ImportFile(ctx, tx, "customers.csv", strings.NewReader(csv), loader.Options{})
		if err != nil || (report.HasRejectedRows() && !commitPartial) {
			return report, err
		}
		return report, tx.Commit(ctx)
	}

	count := func() int {
		var n int
		if err := admin.QueryRow(ctx, "SELECT count(*) FROM "+schema+".customers").Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	t.Run("bulk insert", func(t *testing.T) {
		res, err := upload("email,name,age\na@x.io,A,1\nb@x.io,B,2\nc@x.io,C,\n", false)
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		if res.RowsWritten != 3 || count() != 3 {
			t.Errorf("written = %d, stored = %d, want 3", res.RowsWritten, count())
		}
	})

	t.Run("re-upload updates instead of failing", func(t *testing.T) {
		var before time.Time
		if err := admin.QueryRow(ctx, "SELECT updated_at FROM "+schema+".customers WHERE email = 'a@x.io'").Scan(&before); err != nil {
			t.Fatalf("query: %v", err)
		}

		if _, err := upload("email,name,plan\na@x.io,Renamed,pro\nd@x.io,D,\n", false); err != nil {
			t.Fatalf("upload: %v", err)
		}
		var name, plan string
		var after time.Time
		if err := admin.QueryRow(ctx, "SELECT name, plan, updated_at FROM "+schema+".customers WHERE email = 'a@x.io'").Scan(&name, &plan, &after); err != nil {
			t.Fatalf("query: %v", err)
		}
		if name != "Renamed" || plan != "pro" || count() != 4 {
			t.Errorf("name = %q, plan = %q, count = %d", name, plan, count())
		}
		if !after.After(before) {
			t.Errorf("updated_at did not advance on update: before %v, after %v", before, after)
		}
	})

	t.Run("rejected rows abort the import", func(t *testing.T) {
		before := count()
		report, err := upload("email,name\nnew1@x.io,N1\nnew2@x.io,N2\nbad,N3\n", false)
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		if report.RowsRejected != 1 || report.RowsAccepted != 2 {
			t.Errorf("report = %+v, want 2 accepted and 1 rejected", report)
		}
		if count() != before {
			t.Errorf("count changed from %d to %d despite rollback", before, count())
		}
	})

	t.Run("partial commit keeps the valid rows", func(t *testing.T) {
		before := count()
		report, err := upload("email,name\nnew1@x.io,N1\nbad,N2\n", true)
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		if report.RowsRejected != 1 || count() != before+1 {
			t.Errorf("report = %+v, count = %d, want %d", report, count(), before+1)
		}
	})
}
