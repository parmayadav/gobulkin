package loader_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/parmayadav/gobulkin/pkg/loader"
	"github.com/xuri/excelize/v2"
)

func requireInputError(t *testing.T, err error, wantMessage string) {
	t.Helper()
	var inputErr *loader.InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("err = %v, want an *InputError", err)
	}
	if !strings.Contains(inputErr.Message, wantMessage) {
		t.Errorf("message = %q, want it to contain %q", inputErr.Message, wantMessage)
	}
}

func TestMalformedCSV(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"unterminated quote", "email,name\n\"a@example.com,A\n", "malformed CSV"},
		{"bare quote in a field", "email,name\na@example.com,A\"B\n", "malformed CSV"},
		{"more fields than the header", "email,name\na@example.com,A,extra\n", "malformed CSV"},
		{"fewer fields than the header", "email,name\na@example.com\n", "malformed CSV"},
		{"broken header quote", "\"email,name\na@example.com,A\n", "could not read"},
		{"blank header", " \na@example.com\n", "missing required columns"},
		{"binary content", "PK\x03\x04\x00\x00\xff\xfe\x00garbage\n\x00\x01\x02\n", "missing required columns"},
		{"empty file", "", "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakeDB{}
			ld := newTestLoader(t, db, nil)

			report, err := importCSV(t, ld, tt.content, loader.Options{})

			requireInputError(t, err, tt.want)
			if report == nil || report.Completed {
				t.Errorf("report = %+v, want a non-completed report", report)
			}
		})
	}
}

func TestCSVThatLooksOddButIsValid(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"UTF-8 BOM from Excel", "\ufeffemail,name\na@example.com,A\n"},
		{"CRLF line endings", "email,name\r\na@example.com,A\r\n"},
		{"no trailing newline", "email,name\na@example.com,A"},
		{"blank lines between rows", "email,name\n\na@example.com,A\n\n"},
		{"quoted field with comma", "email,name\na@example.com,\"Smith, A\"\n"},
		{"upper-case header", "EMAIL,NAME\na@example.com,A\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakeDB{}
			ld := newTestLoader(t, db, nil)

			report, err := importCSV(t, ld, tt.content, loader.Options{})

			if err != nil || report.RowsAccepted != 1 || len(db.Rows()) != 1 {
				t.Errorf("report = %+v, err = %v, stored = %d", report, err, len(db.Rows()))
			}
		})
	}
}

func workbook(t *testing.T, sheet string, rows [][]any) []byte {
	t.Helper()
	file := excelize.NewFile()
	defer file.Close()
	if _, err := file.NewSheet(sheet); err != nil {
		t.Fatalf("new sheet: %v", err)
	}
	for i, row := range rows {
		cell, err := excelize.CoordinatesToCellName(1, i+1)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.SetSheetRow(sheet, cell, &row); err != nil {
			t.Fatal(err)
		}
	}
	buf, err := file.WriteToBuffer()
	if err != nil {
		t.Fatalf("write workbook: %v", err)
	}
	return buf.Bytes()
}

func importXLSX(ld testLoader, data []byte, opts loader.Options) (*loader.Report, error) {
	return ld.ImportFile(context.Background(), "data.xlsx", bytes.NewReader(data), opts)
}

func TestXLSXImport(t *testing.T) {
	data := workbook(t, "People", [][]any{
		{"Email", "Name", "Age"},
		{"a@example.com", "Alice", 34},
		{"not-an-email", "Bob", 20},
		{"c@example.com", "Carol", 500},
	})
	db := &fakeDB{}
	ld := newTestLoader(t, db, nil)

	report, err := importXLSX(ld, data, loader.Options{Sheet: "People"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.RowsRead != 3 || report.RowsAccepted != 1 || report.RowsRejected != 2 || len(db.Rows()) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report.Errors[0].Column != "email" || report.Errors[1].Column != "age" {
		t.Errorf("errors = %+v", report.Errors)
	}
	if db.Rows()[0][2] != int64(34) {
		t.Errorf("age = %#v, want int64(34)", db.Rows()[0][2])
	}
}

func TestMalformedXLSX(t *testing.T) {
	valid := workbook(t, "People", [][]any{{"Email", "Name"}, {"a@example.com", "Alice"}})

	tests := []struct {
		name  string
		data  []byte
		sheet string
		want  string
	}{
		{"not a zip", []byte("plainly not a workbook"), "People", "could not read"},
		{"truncated workbook", valid[:len(valid)/2], "People", "could not read"},
		{"empty file", nil, "People", "could not read"},
		{"unknown sheet", valid, "Nope", "could not read"},
		{"empty sheet", workbook(t, "People", nil), "People", "empty"},
		{"duplicate header", workbook(t, "People", [][]any{{"Email", "Name", "EMAIL"}, {"a@example.com", "A", "b@example.com"}}), "People", "duplicate column"},
		{"missing required column", workbook(t, "People", [][]any{{"Email"}, {"a@example.com"}}), "People", "missing required columns: name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ld := newTestLoader(t, &fakeDB{}, nil)

			_, err := importXLSX(ld, tt.data, loader.Options{Sheet: tt.sheet})

			requireInputError(t, err, tt.want)
		})
	}
}

func TestCancelledBeforeStart(t *testing.T) {
	db := &fakeDB{}
	ld := newTestLoader(t, db, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report, err := ld.ImportFile(ctx, "data.csv", strings.NewReader("email,name\na@example.com,A\n"), loader.Options{})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	var inputErr *loader.InputError
	if errors.As(err, &inputErr) {
		t.Error("a cancelled request is not the uploader's fault")
	}
	if report.Completed || len(db.Rows()) != 0 {
		t.Errorf("report = %+v, stored = %d, want nothing written", report, len(db.Rows()))
	}
}

func TestCancelledMidImport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := &fakeDB{AfterCopy: cancel}
	ld := newTestLoader(t, db, func(c *loader.Config) { c.BatchSize = 2 })

	report, err := ld.ImportFile(ctx, "data.csv",
		strings.NewReader("email,name\na@x.io,A\nb@x.io,B\nc@x.io,C\nd@x.io,D\ne@x.io,E\n"), loader.Options{})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if report.Completed {
		t.Error("a cancelled import must not report completion")
	}
	if got := len(db.Rows()); got != 2 {
		t.Errorf("stored = %d, want only the first batch (the caller rolls back)", got)
	}
}

func TestTimeout(t *testing.T) {
	db := &fakeDB{AfterCopy: func() { time.Sleep(60 * time.Millisecond) }}
	ld := newTestLoader(t, db, func(c *loader.Config) {
		c.Timeout = 20 * time.Millisecond
		c.BatchSize = 1
	})

	_, err := importCSV(t, ld, "email,name\na@x.io,A\nb@x.io,B\nc@x.io,C\n", loader.Options{})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if got := len(db.Rows()); got != 1 {
		t.Errorf("stored = %d, want only the batch written before the deadline", got)
	}
}

func TestDuplicateKeys(t *testing.T) {
	db := &fakeDB{}
	ld := newTestLoader(t, db, nil)

	report, err := importCSV(t, ld,
		"email,name\na@example.com,A\nA@EXAMPLE.com ,B\n a@example.com,C\nb@example.com,D\n", loader.Options{})

	if err != nil {
		t.Fatal(err)
	}
	if report.RowsAccepted != 2 || report.RowsRejected != 2 || len(db.Rows()) != 2 {
		t.Fatalf("report = %+v", report)
	}
	for i, wantRow := range []int{2, 3} {
		issue := report.Errors[i]
		if issue.Row != wantRow || issue.Column != "email" || !strings.Contains(issue.Message, "already appears on row 1") {
			t.Errorf("issue %d = %+v", i, issue)
		}
	}
}

func TestPartialFailure(t *testing.T) {
	const csv = "email,name\na@x.io,A\nb@x.io,B\nc@x.io,C\nd@x.io,D\ne@x.io,E\nf@x.io,F\n"

	t.Run("a later batch fails after an earlier one was written", func(t *testing.T) {
		db := &fakeDB{FailOnCall: 2}
		ld := newTestLoader(t, db, func(c *loader.Config) { c.BatchSize = 2 })

		report, err := importCSV(t, ld, csv, loader.Options{})

		if err == nil {
			t.Fatal("expected the write failure")
		}
		var inputErr *loader.InputError
		if errors.As(err, &inputErr) {
			t.Error("a database failure is not the uploader's fault")
		}
		if report.Completed || report.RowsWritten != 0 {
			t.Errorf("report = %+v, want an incomplete report", report)
		}
		if got := len(db.Rows()); got != 2 {
			t.Errorf("stored = %d: the first batch stays in the transaction, so the caller must roll back", got)
		}
	})

	t.Run("the merge step fails", func(t *testing.T) {
		db := &fakeDB{ExecErr: errors.New("fake: exec failed")}
		ld := newTestLoader(t, db, nil)

		report, err := importCSV(t, ld, csv, loader.Options{})

		if err == nil || report.Completed {
			t.Errorf("err = %v, report = %+v, want a failed import", err, report)
		}
	})

	t.Run("rejected rows and a failing write", func(t *testing.T) {
		db := &fakeDB{CopyErr: errors.New("fake: connection lost")}
		ld := newTestLoader(t, db, nil)

		report, err := importCSV(t, ld, "email,name\na@x.io,A\nbad,B\n", loader.Options{})

		if err == nil || report.RowsRejected != 1 || report.Completed {
			t.Errorf("err = %v, report = %+v", err, report)
		}
	})
}

// noLifecycleTx panics if the loader ever calls Commit or Rollback.
type noLifecycleTx struct {
	pgx.Tx
	db *fakeDB
}

func (t noLifecycleTx) CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error) {
	return t.db.CopyFrom(ctx, table, columns, src)
}

func (t noLifecycleTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.db.Exec(ctx, sql, args...)
}

func TestLoaderNeverCommitsOrRollsBack(t *testing.T) {
	ld, err := loader.New(loader.Config{Table: "customers", Columns: testColumns(), Key: []string{"email"}})
	if err != nil {
		t.Fatal(err)
	}

	for name, db := range map[string]*fakeDB{
		"success":       {},
		"write failure": {CopyErr: errors.New("fake: boom")},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("the loader touched the transaction lifecycle: %v", r)
				}
			}()
			_, _ = ld.ImportFile(context.Background(), noLifecycleTx{db: db}, "data.csv",
				strings.NewReader("email,name\na@example.com,A\n"), loader.Options{})
		})
	}
}
