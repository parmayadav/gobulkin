package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type Conn interface {
	CopyFrom(
		context.Context,
		pgx.Identifier,
		[]string,
		pgx.CopyFromSource,
	) (int64, error)
}

type Writer struct {
	conn Conn
}

func NewWriter(conn Conn) *Writer {
	return &Writer{conn: conn}
}

func (w *Writer) Copy(
	ctx context.Context,
	table pgx.Identifier,
	columns []string,
	rows [][]any,
) (int64, error) {
	return w.conn.CopyFrom(
		ctx, table, columns, pgx.CopyFromRows(rows),
	)
}

// BatchFunc returns a core.WriteFunc that COPYs each batch into table.
func (w *Writer) BatchFunc(
	table pgx.Identifier,
	columns []string,
) func(context.Context, [][]any) (int64, error) {
	return func(ctx context.Context, rows [][]any) (int64, error) {
		return w.Copy(ctx, table, columns, rows)
	}
}
