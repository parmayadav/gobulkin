package loader

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/parmayadav/gobulkin/pkg/core"
	"github.com/xuri/excelize/v2"
)

var _ core.Source = (*xlsxReader)(nil)

const testSheet = "People"

func workbookBytes(t *testing.T, rows [][]any) []byte {
	t.Helper()
	workbook := excelize.NewFile()
	defer workbook.Close()

	if _, err := workbook.NewSheet(testSheet); err != nil {
		t.Fatalf("new sheet: %v", err)
	}
	for i, row := range rows {
		cell, err := excelize.CoordinatesToCellName(1, i+1)
		if err != nil {
			t.Fatalf("cell name: %v", err)
		}
		if err := workbook.SetSheetRow(testSheet, cell, &row); err != nil {
			t.Fatalf("set row: %v", err)
		}
	}

	buf, err := workbook.WriteToBuffer()
	if err != nil {
		t.Fatalf("write workbook: %v", err)
	}
	return buf.Bytes()
}

func TestXLSXReader_ReadsHeaderAndNumberedRows(t *testing.T) {
	data := workbookBytes(t, [][]any{
		{"email", "name", "age"},
		{"a@x.io", "Alice", 34},
		{"b@x.io", "Bob", 20},
	})

	src, err := newXLSXReader(bytes.NewReader(data), testSheet)
	if err != nil {
		t.Fatalf("newXLSXReader: %v", err)
	}
	defer src.Close()

	if want := []string{"email", "name", "age"}; !reflect.DeepEqual(src.Headers(), want) {
		t.Errorf("headers = %v, want %v", src.Headers(), want)
	}

	for i, want := range [][]string{{"a@x.io", "Alice", "34"}, {"b@x.io", "Bob", "20"}} {
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

func TestXLSXReader_HeadersReturnsACopy(t *testing.T) {
	data := workbookBytes(t, [][]any{{"a", "b"}, {1, 2}})
	src, err := newXLSXReader(bytes.NewReader(data), testSheet)
	if err != nil {
		t.Fatalf("newXLSXReader: %v", err)
	}
	defer src.Close()

	src.Headers()[0] = "changed"

	if src.Headers()[0] != "a" {
		t.Error("mutating the returned slice must not affect the source")
	}
}

func TestXLSXReader_Errors(t *testing.T) {
	t.Run("not a workbook", func(t *testing.T) {
		if _, err := newXLSXReader(strings.NewReader("plainly not a zip"), testSheet); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("unknown sheet", func(t *testing.T) {
		data := workbookBytes(t, [][]any{{"a"}})
		if _, err := newXLSXReader(bytes.NewReader(data), "Nope"); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("empty sheet is EOF", func(t *testing.T) {
		data := workbookBytes(t, nil)
		if _, err := newXLSXReader(bytes.NewReader(data), testSheet); !errors.Is(err, io.EOF) {
			t.Fatalf("err = %v, want io.EOF", err)
		}
	})

	t.Run("failing reader", func(t *testing.T) {
		boom := errors.New("connection reset")
		if _, err := newXLSXReader(io.MultiReader(strings.NewReader("PK"), errReader{boom}), testSheet); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
	})
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestXLSXReader_CancelledContextStopsReading(t *testing.T) {
	data := workbookBytes(t, [][]any{{"a"}, {"1"}})
	src, err := newXLSXReader(bytes.NewReader(data), testSheet)
	if err != nil {
		t.Fatalf("newXLSXReader: %v", err)
	}
	defer src.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := src.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
