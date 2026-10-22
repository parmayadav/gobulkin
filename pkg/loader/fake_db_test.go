package loader_test

import (
	"context"
	"errors"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeDB records what is written through it.
type fakeDB struct {
	// CopyErr, when set, makes every CopyFrom fail.
	CopyErr error
	// ExecErr, when set, makes every Exec fail.
	ExecErr error
	// FailOnCall, when positive, makes that CopyFrom call (1-based) fail.
	FailOnCall int
	// AfterCopy runs after each successful CopyFrom.
	AfterCopy func()

	mu         sync.Mutex
	rows       [][]any
	statements []string
	calls      int
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
	d.calls++
	if d.FailOnCall > 0 && d.calls == d.FailOnCall {
		d.mu.Unlock()
		return 0, errors.New("fake: copy failed")
	}

	var copied int64
	for src.Next() {
		values, err := src.Values()
		if err != nil {
			d.mu.Unlock()
			return copied, err
		}
		d.rows = append(d.rows, values)
		copied++
	}
	err := src.Err()
	d.mu.Unlock()

	if d.AfterCopy != nil {
		d.AfterCopy()
	}
	return copied, err
}

func (d *fakeDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	if d.ExecErr != nil {
		return pgconn.CommandTag{}, d.ExecErr
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.statements = append(d.statements, sql)
	return pgconn.NewCommandTag("INSERT 0 0"), nil
}
