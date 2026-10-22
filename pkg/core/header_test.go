package core

import (
	"reflect"
	"testing"
)

func TestNewHeader_NormalisesNames(t *testing.T) {
	h := NewHeader([]string{"\ufeffEmail", "  NAME ", "", "Plan"})

	if want := []string{"email", "name", "", "plan"}; !reflect.DeepEqual(h.Names(), want) {
		t.Errorf("names = %q, want %q", h.Names(), want)
	}
}

func TestHeader_Index(t *testing.T) {
	h := NewHeader([]string{"Email", "Name", "", "email"})

	tests := []struct {
		column string
		want   int
		found  bool
	}{
		{"email", 0, true},
		{"EMAIL", 0, true},
		{"  Name ", 1, true},
		{"missing", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.column, func(t *testing.T) {
			got, ok := h.Index(tt.column)
			if got != tt.want || ok != tt.found {
				t.Errorf("Index(%q) = %d, %v; want %d, %v", tt.column, got, ok, tt.want, tt.found)
			}
		})
	}
}

func TestHeader_DuplicateColumnFirstWins(t *testing.T) {
	h := NewHeader([]string{"a", "b", "A"})

	if i, _ := h.Index("a"); i != 0 {
		t.Errorf("Index(a) = %d, want 0", i)
	}
}

func TestHeader_Missing(t *testing.T) {
	h := NewHeader([]string{"email", "age"})

	if got, want := h.Missing("name", "EMAIL", "plan"), []string{"name", "plan"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Missing = %v, want %v", got, want)
	}
	if got := h.Missing("email"); got != nil {
		t.Errorf("Missing = %v, want nil when nothing is missing", got)
	}
}

func TestHeader_NamesReturnsACopy(t *testing.T) {
	h := NewHeader([]string{"a"})

	h.Names()[0] = "changed"

	if h.Names()[0] != "a" {
		t.Error("mutating the returned slice must not affect the header")
	}
}

func TestHeader_NilIsEmpty(t *testing.T) {
	var h *Header

	if h.Names() != nil {
		t.Error("nil header should have no names")
	}
	if _, ok := h.Index("a"); ok {
		t.Error("nil header should find no columns")
	}
	if got := h.Missing("a", "b"); len(got) != 2 {
		t.Errorf("Missing = %v, want every column reported", got)
	}
}

func TestRecord_Value(t *testing.T) {
	rec := Record{Values: []string{"a@x.io", "Alice"}, Header: NewHeader([]string{"email", "name", "age"})}

	tests := []struct {
		column string
		want   string
	}{
		{"email", "a@x.io"},
		{"Name", "Alice"},
		{"age", ""},  // column exists but the row is short
		{"nope", ""}, // column does not exist
	}
	for _, tt := range tests {
		if got := rec.Value(tt.column); got != tt.want {
			t.Errorf("Value(%q) = %q, want %q", tt.column, got, tt.want)
		}
	}

	if got := (Record{Values: []string{"x"}}).Value("email"); got != "" {
		t.Errorf("Value on a record without a header = %q, want empty", got)
	}
}

func TestRecord_Set(t *testing.T) {
	rec := Record{Values: []string{"a@x.io"}, Header: NewHeader([]string{"email", "name", "age"})}

	if !rec.Set("age", "30") {
		t.Fatal("Set on an existing column reported false")
	}
	if want := []string{"a@x.io", "", "30"}; !reflect.DeepEqual(rec.Values, want) {
		t.Errorf("values = %q, want %q (short row padded)", rec.Values, want)
	}

	if rec.Set("nope", "x") {
		t.Error("Set on a missing column reported true")
	}
	if len(rec.Values) != 3 {
		t.Errorf("a failed Set must not change the row: %q", rec.Values)
	}

	if (&Record{}).Set("email", "x") {
		t.Error("Set without a header reported true")
	}
}
