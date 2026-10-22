package loader

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/parmayadav/gobulkin/pkg/core"
)

var _ core.Source = (*csvReader)(nil)

func TestCSVReader_ReadsHeaderAndNumberedRows(t *testing.T) {
	src, err := newCSVReader(strings.NewReader("email,name\na@x.io,Alice\nb@x.io,Bob\n"))
	if err != nil {
		t.Fatalf("newCSVReader: %v", err)
	}
	defer src.Close()

	if want := []string{"email", "name"}; !reflect.DeepEqual(src.Headers(), want) {
		t.Errorf("headers = %v, want %v", src.Headers(), want)
	}

	for i, want := range [][]string{{"a@x.io", "Alice"}, {"b@x.io", "Bob"}} {
		rec, err := src.Next(context.Background())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if rec.Number != i+1 || !reflect.DeepEqual(rec.Values, want) {
			t.Errorf("record = %+v, want number %d values %v", rec, i+1, want)
		}
	}

	if _, err := src.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want io.EOF", err)
	}
}

func TestCSVReader_EmptyInputIsEOF(t *testing.T) {
	if _, err := newCSVReader(strings.NewReader("")); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestCSVReader_HeadersReturnsACopy(t *testing.T) {
	src, err := newCSVReader(strings.NewReader("a,b\n1,2\n"))
	if err != nil {
		t.Fatalf("newCSVReader: %v", err)
	}

	src.Headers()[0] = "changed"

	if src.Headers()[0] != "a" {
		t.Error("mutating the returned slice must not affect the source")
	}
}

func TestCSVReader_RaggedRowIsAFieldCountError(t *testing.T) {
	src, err := newCSVReader(strings.NewReader("a,b\n1\n"))
	if err != nil {
		t.Fatalf("newCSVReader: %v", err)
	}

	_, err = src.Next(context.Background())

	var parseErr *csv.ParseError
	if !errors.As(err, &parseErr) || !errors.Is(err, csv.ErrFieldCount) {
		t.Fatalf("err = %v, want a *csv.ParseError wrapping ErrFieldCount", err)
	}
}

func TestCSVReader_CancelledContextStopsReading(t *testing.T) {
	src, err := newCSVReader(strings.NewReader("a\n1\n"))
	if err != nil {
		t.Fatalf("newCSVReader: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := src.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestCSVReader_QuotedFieldsAndEmbeddedNewlines(t *testing.T) {
	src, err := newCSVReader(strings.NewReader("name,note\n\"Smith, A\",\"line1\nline2\"\n"))
	if err != nil {
		t.Fatalf("newCSVReader: %v", err)
	}

	rec, err := src.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if want := []string{"Smith, A", "line1\nline2"}; !reflect.DeepEqual(rec.Values, want) {
		t.Errorf("values = %q, want %q", rec.Values, want)
	}
}
