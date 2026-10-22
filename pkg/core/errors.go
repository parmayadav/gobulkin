package core

import "fmt"

// RowError describes why one row was rejected. Hooks return it, wrapping ErrInvalidRecord
// in Err, to reject a row; Import fills in Row.
type RowError struct {
	Row    int
	Column string
	Value  string
	Err    error
}

func (e *RowError) Error() string {
	if e.Column != "" {
		return fmt.Sprintf("row %d, column %q: %v",
			e.Row, e.Column, e.Err)
	}
	return fmt.Sprintf("row %d: %v", e.Row, e.Err)
}

func (e *RowError) Unwrap() error {
	return e.Err
}
