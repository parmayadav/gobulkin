package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeTx struct {
	fakeConn
	execs   []string
	execErr map[int]error
	tag     string
}

func (f *fakeTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, sql)
	if err := f.execErr[len(f.execs)]; err != nil {
		return pgconn.CommandTag{}, err
	}
	return pgconn.NewCommandTag(f.tag), nil
}

func newUpserter(t *testing.T, tx *fakeTx, spec UpsertSpec) *Upserter {
	t.Helper()
	up, err := NewUpserter(tx, spec)
	if err != nil {
		t.Fatalf("NewUpserter: %v", err)
	}
	return up
}

var customersSpec = UpsertSpec{
	Table:   pgx.Identifier{"customers"},
	Columns: []string{"email", "name", "age"},
	Key:     []string{"email"},
}

func TestNewUpserter_ValidatesSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    UpsertSpec
		wantErr string
	}{
		{"valid", customersSpec, ""},
		{"no table", UpsertSpec{Columns: []string{"a"}, Key: []string{"a"}}, "needs a table"},
		{"no columns", UpsertSpec{Table: pgx.Identifier{"t"}, Key: []string{"a"}}, "needs a table"},
		{"no key", UpsertSpec{Table: pgx.Identifier{"t"}, Columns: []string{"a"}}, "needs a table"},
		{"key not in columns", UpsertSpec{Table: pgx.Identifier{"t"}, Columns: []string{"a"}, Key: []string{"b"}}, `key column "b"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewUpserter(&fakeTx{}, tt.spec)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestUpserter_StagesOnceAndCopiesIntoStage(t *testing.T) {
	tx := &fakeTx{}
	up := newUpserter(t, tx, customersSpec)

	for _, batch := range [][][]any{{{"a@x.io", "A", 1}}, {{"b@x.io", "B", nil}, {"c@x.io", "C", 3}}} {
		if _, err := up.Write(context.Background(), batch); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	if len(tx.execs) != 1 {
		t.Fatalf("staging table created %d times, want once: %v", len(tx.execs), tx.execs)
	}
	want := `CREATE TEMP TABLE ` + up.stage.Sanitize() + ` ON COMMIT DROP AS SELECT "email", "name", "age" FROM "customers" WITH NO DATA`
	if tx.execs[0] != want {
		t.Errorf("create SQL =\n%s\nwant\n%s", tx.execs[0], want)
	}

	if len(tx.calls) != 2 {
		t.Fatalf("copy calls = %d, want 2", len(tx.calls))
	}
	for _, call := range tx.calls {
		if !reflect.DeepEqual(call.table, up.stage) || !reflect.DeepEqual(call.columns, customersSpec.Columns) {
			t.Errorf("copy went to %v %v, want the staging table and spec columns", call.table, call.columns)
		}
	}
}

func TestUpserter_MergeSQL(t *testing.T) {
	tests := []struct {
		name string
		spec UpsertSpec
		want string
	}{
		{
			"updates every non-key column",
			customersSpec,
			`INSERT INTO "customers" ("email", "name", "age") SELECT "email", "name", "age" FROM %s ` +
				`ON CONFLICT ("email") DO UPDATE SET "name" = EXCLUDED."name", "age" = EXCLUDED."age"`,
		},
		{
			"key-only table does nothing on conflict",
			UpsertSpec{Table: pgx.Identifier{"tags"}, Columns: []string{"tag"}, Key: []string{"tag"}},
			`INSERT INTO "tags" ("tag") SELECT "tag" FROM %s ON CONFLICT ("tag") DO NOTHING`,
		},
		{
			"composite key and schema-qualified table",
			UpsertSpec{Table: pgx.Identifier{"app", "stock"}, Columns: []string{"sku", "site", "qty"}, Key: []string{"sku", "site"}},
			`INSERT INTO "app"."stock" ("sku", "site", "qty") SELECT "sku", "site", "qty" FROM %s ` +
				`ON CONFLICT ("sku", "site") DO UPDATE SET "qty" = EXCLUDED."qty"`,
		},
		{
			"identifiers are quoted so they cannot inject SQL",
			UpsertSpec{Table: pgx.Identifier{`t"; DROP TABLE x; --`}, Columns: []string{`k"`, `v`}, Key: []string{`k"`}},
			`INSERT INTO "t""; DROP TABLE x; --" ("k""", "v") SELECT "k""", "v" FROM %s ` +
				`ON CONFLICT ("k""") DO UPDATE SET "v" = EXCLUDED."v"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := &fakeTx{tag: "INSERT 0 1"}
			up := newUpserter(t, tx, tt.spec)
			if _, err := up.Write(context.Background(), [][]any{{1}}); err != nil {
				t.Fatalf("Write: %v", err)
			}

			if _, err := up.Merge(context.Background()); err != nil {
				t.Fatalf("Merge: %v", err)
			}

			want := strings.Replace(tt.want, "%s", up.stage.Sanitize(), 1)
			if got := tx.execs[len(tx.execs)-1]; got != want {
				t.Errorf("merge SQL =\n%s\nwant\n%s", got, want)
			}
		})
	}
}

func TestUpserter_MergeReportsRowsAffected(t *testing.T) {
	tx := &fakeTx{tag: "INSERT 0 7"}
	up := newUpserter(t, tx, customersSpec)
	if _, err := up.Write(context.Background(), [][]any{{"a", "b", 1}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	n, err := up.Merge(context.Background())

	if err != nil || n != 7 {
		t.Errorf("Merge = %d, %v; want 7, nil", n, err)
	}
}

func TestUpserter_MergeWithNothingStagedIsANoOp(t *testing.T) {
	tx := &fakeTx{}
	up := newUpserter(t, tx, customersSpec)

	n, err := up.Merge(context.Background())

	if err != nil || n != 0 || len(tx.execs) != 0 {
		t.Errorf("Merge = %d, %v with %d statements; want 0, nil and none", n, err, len(tx.execs))
	}
}

func TestUpserter_IsSingleUse(t *testing.T) {
	tx := &fakeTx{tag: "INSERT 0 1"}
	up := newUpserter(t, tx, customersSpec)
	if _, err := up.Write(context.Background(), [][]any{{"a", "b", 1}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := up.Merge(context.Background()); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	if _, err := up.Write(context.Background(), [][]any{{"c", "d", 2}}); !errors.Is(err, errUpserterDone) {
		t.Errorf("Write after Merge: err = %v, want errUpserterDone", err)
	}
	if _, err := up.Merge(context.Background()); !errors.Is(err, errUpserterDone) {
		t.Errorf("second Merge: err = %v, want errUpserterDone", err)
	}
}

func TestUpserter_PropagatesErrors(t *testing.T) {
	boom := errors.New("boom")

	t.Run("create staging table", func(t *testing.T) {
		up := newUpserter(t, &fakeTx{execErr: map[int]error{1: boom}}, customersSpec)
		if _, err := up.Write(context.Background(), [][]any{{1}}); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
	})

	t.Run("copy", func(t *testing.T) {
		up := newUpserter(t, &fakeTx{fakeConn: fakeConn{err: boom}}, customersSpec)
		if _, err := up.Write(context.Background(), [][]any{{1}}); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
	})

	t.Run("merge", func(t *testing.T) {
		up := newUpserter(t, &fakeTx{execErr: map[int]error{2: boom}}, customersSpec)
		if _, err := up.Write(context.Background(), [][]any{{"a", "b", 1}}); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if _, err := up.Merge(context.Background()); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
	})
}

func TestUpserter_EachInstanceGetsItsOwnStagingTable(t *testing.T) {
	a := newUpserter(t, &fakeTx{}, customersSpec)
	b := newUpserter(t, &fakeTx{}, customersSpec)

	if reflect.DeepEqual(a.stage, b.stage) {
		t.Errorf("two upserters share the staging table %v", a.stage)
	}
}
