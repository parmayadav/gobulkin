package loader_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/parmayadav/gobulkin/pkg/loader"
)

// Runs against a real PostgreSQL when TEST_DATABASE_URL is set; each test gets a throwaway schema.
type pgEnv struct {
	conn  *pgx.Conn
	count func() int
}

func newPGEnv(t *testing.T) pgEnv {
	t.Helper()
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
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	schema := fmt.Sprintf("gobulkin_it_%d", time.Now().UnixNano())
	ddl := fmt.Sprintf(`CREATE SCHEMA %[1]s;
		CREATE TABLE %[1]s.people (
			email text PRIMARY KEY,
			name  text NOT NULL CHECK (char_length(name) <= 5),
			age   integer
		)`, schema)
	if _, err := admin.Exec(ctx, ddl); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.RuntimeParams["search_path"] = schema
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	return pgEnv{conn: conn, count: func() int {
		var n int
		if err := admin.QueryRow(context.Background(), "SELECT count(*) FROM "+schema+".people").Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}}
}

func peopleLoader(t *testing.T, withKey bool, batchSize int) *loader.Loader {
	t.Helper()
	cfg := loader.Config{
		Table:     "people",
		BatchSize: batchSize,
		Columns: []loader.Column{
			{Name: "email", Required: true, Case: loader.Lower, Check: loader.Email},
			{Name: "name", Required: true},
			{Name: "age", Type: loader.Int},
		},
	}
	if withKey {
		cfg.Key = []string{"email"}
	}
	ld, err := loader.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ld
}

// importAndFinish imports csv in its own transaction, committing when commit(report) is true.
func (e pgEnv) importAndFinish(t *testing.T, ctx context.Context, ld *loader.Loader, csv string, commit func(*loader.Report) bool) (*loader.Report, error) {
	t.Helper()
	tx, err := e.conn.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	report, err := ld.ImportFile(ctx, tx, "people.csv", strings.NewReader(csv), loader.Options{})
	if err != nil || !commit(report) {
		return report, err
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return report, nil
}

func always(*loader.Report) bool { return true }

func TestIntegration_CommitAndRollback(t *testing.T) {
	env := newPGEnv(t)
	ld := peopleLoader(t, true, 0)
	const csv = "email,name,age\na@x.io,Ann,30\nb@x.io,Bob,\nc@x.io,Cy,41\n"

	if _, err := env.importAndFinish(t, context.Background(), ld, csv, func(*loader.Report) bool { return false }); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := env.count(); got != 0 {
		t.Fatalf("rows after a rollback = %d, want 0", got)
	}

	if _, err := env.importAndFinish(t, context.Background(), ld, csv, always); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := env.count(); got != 3 {
		t.Errorf("rows after a commit = %d, want 3", got)
	}
}

func TestIntegration_RejectedRowsNeverReachTheDatabase(t *testing.T) {
	env := newPGEnv(t)
	ld := peopleLoader(t, false, 0)

	report, err := env.importAndFinish(t, context.Background(), ld,
		"email,name,age\na@x.io,Ann,30\nnot-an-email,Bob,1\nc@x.io,,2\nd@x.io,Di,x\ne@x.io,Eve,5\n", always)

	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if report.RowsRejected != 3 || report.RowsAccepted != 2 || env.count() != 2 {
		t.Errorf("report = %+v, stored = %d, want 2 accepted and 3 rejected", report, env.count())
	}
}

func TestIntegration_DatabaseFailureMidImportThenRollback(t *testing.T) {
	// name "toolong" passes the loader but violates the table's CHECK constraint.
	const csv = "email,name,age\na@x.io,Ann,1\nb@x.io,Bob,2\nc@x.io,Cy,3\nd@x.io,Di,4\ne@x.io,toolong,5\n"

	for _, withKey := range []bool{false, true} {
		t.Run(fmt.Sprintf("key=%v", withKey), func(t *testing.T) {
			env := newPGEnv(t)
			ld := peopleLoader(t, withKey, 2)

			report, err := env.importAndFinish(t, context.Background(), ld, csv, always)

			if err == nil {
				t.Fatal("expected the constraint violation")
			}
			var inputErr *loader.InputError
			if errors.As(err, &inputErr) {
				t.Error("a database failure must not be reported as bad input")
			}
			if report.Completed {
				t.Error("a failed import must not report completion")
			}
			if got := env.count(); got != 0 {
				t.Errorf("rows after the failed import = %d, want 0 (rolled back)", got)
			}
		})
	}
}

func TestIntegration_ExistingRows(t *testing.T) {
	env := newPGEnv(t)
	seed := peopleLoader(t, false, 0)
	if _, err := env.importAndFinish(t, context.Background(), seed, "email,name\na@x.io,Ann\n", always); err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Run("without a key a duplicate fails and rolls back", func(t *testing.T) {
		_, err := env.importAndFinish(t, context.Background(), seed, "email,name\nb@x.io,Bob\na@x.io,Again\n", always)
		if err == nil {
			t.Fatal("expected a unique violation")
		}
		if got := env.count(); got != 1 {
			t.Errorf("rows = %d, want the original 1", got)
		}
	})

	t.Run("with a key it updates the existing row", func(t *testing.T) {
		keyed := peopleLoader(t, true, 0)
		report, err := env.importAndFinish(t, context.Background(), keyed, "email,name\na@x.io,Anna\nb@x.io,Bob\n", always)
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		var name string
		if err := env.conn.QueryRow(context.Background(), "SELECT name FROM people WHERE email = 'a@x.io'").Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != "Anna" || env.count() != 2 || report.RowsWritten != 2 {
			t.Errorf("name = %q, rows = %d, report = %+v", name, env.count(), report)
		}
	})
}

func TestIntegration_ManyBatches(t *testing.T) {
	env := newPGEnv(t)
	ld := peopleLoader(t, true, 4)
	var csv strings.Builder
	csv.WriteString("email,name,age\n")
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&csv, "u%d@x.io,N%d,%d\n", i, i, i)
	}

	report, err := env.importAndFinish(t, context.Background(), ld, csv.String(), always)

	if err != nil || report.RowsWritten != 25 || env.count() != 25 {
		t.Errorf("err = %v, report = %+v, stored = %d", err, report, env.count())
	}
}

func TestIntegration_CancelledContext(t *testing.T) {
	env := newPGEnv(t)
	ld := peopleLoader(t, true, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := env.importAndFinish(t, ctx, ld, "email,name\na@x.io,Ann\n", always)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if got := env.count(); got != 0 {
		t.Errorf("rows = %d, want 0", got)
	}
}
