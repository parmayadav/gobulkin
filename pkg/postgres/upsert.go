package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Tx is the subset of pgx.Tx that Upserter uses.
type Tx interface {
	Conn
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// UpsertSpec says where rows go and which columns identify an existing row.
type UpsertSpec struct {
	Table   pgx.Identifier
	Columns []string
	// Key is a unique constraint on Table; every column must also be in Columns.
	Key []string
}

var stageCounter atomic.Uint64

var errUpserterDone = errors.New("postgres: upserter already merged")

// Upserter stages batches in a temporary table and merges them into the target with one
// insert-or-update, so a re-imported file updates rows instead of violating the key.
// Use it as the write function of core.Import, then call Merge once:
//
//	up, _ := postgres.NewUpserter(tx, spec)
//	_, err := core.Import(ctx, src, up.Write, mapRow, opts)
//	if err == nil {
//	    _, err = up.Merge(ctx)
//	}
//
// A key must not repeat within one import. Use one Upserter per import and transaction.
type Upserter struct {
	tx     Tx
	spec   UpsertSpec
	stage  pgx.Identifier
	staged bool
	done   bool
}

func NewUpserter(tx Tx, spec UpsertSpec) (*Upserter, error) {
	if len(spec.Table) == 0 || len(spec.Columns) == 0 || len(spec.Key) == 0 {
		return nil, errors.New("postgres: upsert spec needs a table, columns and a key")
	}
	columns := make(map[string]struct{}, len(spec.Columns))
	for _, column := range spec.Columns {
		columns[column] = struct{}{}
	}
	for _, key := range spec.Key {
		if _, ok := columns[key]; !ok {
			return nil, fmt.Errorf("postgres: key column %q is not in Columns", key)
		}
	}

	name := fmt.Sprintf("gobulkin_stage_%d", stageCounter.Add(1))
	return &Upserter{tx: tx, spec: spec, stage: pgx.Identifier{name}}, nil
}

// Write stages one batch; it matches core.WriteFunc.
func (u *Upserter) Write(ctx context.Context, rows [][]any) (int64, error) {
	if u.done {
		return 0, errUpserterDone
	}
	if !u.staged {
		if _, err := u.tx.Exec(ctx, u.createStageSQL()); err != nil {
			return 0, fmt.Errorf("create staging table: %w", err)
		}
		u.staged = true
	}
	return NewWriter(u.tx).Copy(ctx, u.stage, u.spec.Columns, rows)
}

// Merge applies the staged rows and returns how many were inserted or updated.
func (u *Upserter) Merge(ctx context.Context) (int64, error) {
	if u.done {
		return 0, errUpserterDone
	}
	u.done = true
	if !u.staged {
		return 0, nil
	}

	tag, err := u.tx.Exec(ctx, u.mergeSQL())
	if err != nil {
		return 0, fmt.Errorf("merge staged rows: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (u *Upserter) createStageSQL() string {
	return fmt.Sprintf("CREATE TEMP TABLE %s ON COMMIT DROP AS SELECT %s FROM %s WITH NO DATA",
		u.stage.Sanitize(), quoteList(u.spec.Columns), u.spec.Table.Sanitize())
}

func (u *Upserter) mergeSQL() string {
	columns := quoteList(u.spec.Columns)
	sql := fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s ON CONFLICT (%s) ",
		u.spec.Table.Sanitize(), columns, columns, u.stage.Sanitize(), quoteList(u.spec.Key))

	updates := u.updateAssignments()
	if len(updates) == 0 {
		return sql + "DO NOTHING"
	}
	return sql + "DO UPDATE SET " + strings.Join(updates, ", ")
}

// updateAssignments covers every column except the key.
func (u *Upserter) updateAssignments() []string {
	isKey := make(map[string]struct{}, len(u.spec.Key))
	for _, key := range u.spec.Key {
		isKey[key] = struct{}{}
	}

	var assignments []string
	for _, column := range u.spec.Columns {
		if _, skip := isKey[column]; skip {
			continue
		}
		quoted := pgx.Identifier{column}.Sanitize()
		assignments = append(assignments, quoted+" = EXCLUDED."+quoted)
	}
	return assignments
}

func quoteList(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = pgx.Identifier{name}.Sanitize()
	}
	return strings.Join(quoted, ", ")
}
