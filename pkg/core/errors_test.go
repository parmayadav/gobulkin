package core

import (
	"errors"
	"io"
	"testing"
)

func TestRowError_Message(t *testing.T) {
	tests := []struct {
		name string
		err  *RowError
		want string
	}{
		{"with column", &RowError{Row: 2, Column: "email", Err: errors.New("required")}, `row 2, column "email": required`},
		{"without column", &RowError{Row: 5, Err: errors.New("bad row")}, "row 5: bad row"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRowError_UnwrapExposesTheCause(t *testing.T) {
	cause := io.ErrUnexpectedEOF
	err := error(&RowError{Row: 1, Err: cause})

	if !errors.Is(err, cause) {
		t.Error("errors.Is should reach the wrapped cause")
	}
	var rowErr *RowError
	if !errors.As(err, &rowErr) || rowErr.Row != 1 {
		t.Errorf("errors.As did not recover the RowError: %v", err)
	}
}
