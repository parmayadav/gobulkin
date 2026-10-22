package loader_test

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeDB records what is written through it.
type fakeDB struct {
	// CopyErr, when set, makes every CopyFrom fail.
	CopyErr error

	mu         sync.Mutex
	rows       [][]any
	statements []string
}

func (d *fakeDB) Rows() [][]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rows
}

func (d *fakeDB) Statements() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.statements...)
}

func (d *fakeDB) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, src pgx.CopyFromSource) (int64, error) {
	if d.CopyErr != nil {
		return 0, d.CopyErr
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	var copied int64
	for src.Next() {
		values, err := src.Values()
		if err != nil {
			return copied, err
		}
		d.rows = append(d.rows, values)
		copied++
	}
	return copied, src.Err()
}

func (d *fakeDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.statements = append(d.statements, sql)
	return pgconn.NewCommandTag("INSERT 0 0"), nil
}
