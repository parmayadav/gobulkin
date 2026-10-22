package postgres

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
)

type copyCall struct {
	table   pgx.Identifier
	columns []string
	rows    [][]any
}

type fakeConn struct {
	calls []copyCall
	err   error
}

func (f *fakeConn) CopyFrom(
	_ context.Context,
	table pgx.Identifier,
	columns []string,
	src pgx.CopyFromSource,
) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}

	call := copyCall{table: table, columns: columns}
	for src.Next() {
		values, err := src.Values()
		if err != nil {
			return 0, err
		}
		call.rows = append(call.rows, values)
	}
	f.calls = append(f.calls, call)
	return int64(len(call.rows)), nil
}

func TestWriter_CopyPassesTableColumnsAndRows(t *testing.T) {
	conn := &fakeConn{}
	rows := [][]any{{"a@x.io", 1}, {"b@x.io", nil}}

	n, err := NewWriter(conn).Copy(context.Background(), pgx.Identifier{"customers"}, []string{"email", "age"}, rows)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}

	if n != 2 || len(conn.calls) != 1 {
		t.Fatalf("rows written = %d, calls = %d", n, len(conn.calls))
	}
	got := conn.calls[0]
	if !reflect.DeepEqual(got.table, pgx.Identifier{"customers"}) ||
		!reflect.DeepEqual(got.columns, []string{"email", "age"}) ||
		!reflect.DeepEqual(got.rows, rows) {
		t.Errorf("call = %+v", got)
	}
}

func TestWriter_CopyReturnsConnectionError(t *testing.T) {
	boom := errors.New("connection reset")

	_, err := NewWriter(&fakeConn{err: boom}).Copy(context.Background(), pgx.Identifier{"t"}, []string{"c"}, [][]any{{1}})

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestWriter_BatchFuncBindsTableAndColumns(t *testing.T) {
	conn := &fakeConn{}
	write := NewWriter(conn).BatchFunc(pgx.Identifier{"stage"}, []string{"a", "b"})

	for _, batch := range [][][]any{{{"x", 1}}, {{"y", 2}, {"z", 3}}} {
		if _, err := write(context.Background(), batch); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	if len(conn.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(conn.calls))
	}
	for _, call := range conn.calls {
		if !reflect.DeepEqual(call.table, pgx.Identifier{"stage"}) || !reflect.DeepEqual(call.columns, []string{"a", "b"}) {
			t.Errorf("call = %+v, want table stage and columns a, b", call)
		}
	}
	if len(conn.calls[1].rows) != 2 {
		t.Errorf("second batch rows = %d, want 2", len(conn.calls[1].rows))
	}
}

func TestWriter_BatchFuncPropagatesErrors(t *testing.T) {
	boom := errors.New("disk full")
	write := NewWriter(&fakeConn{err: boom}).BatchFunc(pgx.Identifier{"t"}, []string{"c"})

	if _, err := write(context.Background(), [][]any{{1}}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}
