package core

import "context"

// Source yields the rows of one file.
type Source interface {
	// Headers returns the column names, or nil when the source has no header row.
	Headers() []string
	// Next returns the next row, or an error wrapping io.EOF when there are none left.
	Next(context.Context) (Record, error)
	Close() error
}
