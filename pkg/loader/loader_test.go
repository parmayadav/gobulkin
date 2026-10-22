package loader_test

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/parmayadav/gobulkin/pkg/loader"
)

func testColumns() []loader.Column {
	return []loader.Column{
		{Name: "email", Required: true, Case: loader.Lower, Check: loader.Email},
		{Name: "name", Required: true, MaxLength: 10},
		{Name: "age", Type: loader.Int, Range: &loader.Range{Min: 0, Max: 130}},
		{Name: "country", Case: loader.Upper, Pattern: regexp.MustCompile(`^[A-Z]{2}$`), PatternMessage: "must be a two-letter code"},
		{Name: "plan", Case: loader.Lower, OneOf: []string{"free", "pro"}, Default: "free"},
	}
}

// testLoader pairs a Loader with the fake transaction its imports write through.
type testLoader struct {
	*loader.Loader
	db *fakeDB
}

func (l testLoader) ImportFile(ctx context.Context, filename string, content io.Reader, opts loader.Options) (*loader.Report, error) {
	return l.Loader.ImportFile(ctx, l.db, filename, content, opts)
}

func newTestLoader(t *testing.T, db *fakeDB, mutate func(*loader.Config)) testLoader {
	t.Helper()
	cfg := loader.Config{Table: "customers", Columns: testColumns(), Key: []string{"email"}}
	if mutate != nil {
		mutate(&cfg)
	}
	ld, err := loader.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return testLoader{Loader: ld, db: db}
}

func importCSV(t *testing.T, ld testLoader, csv string, opts loader.Options) (*loader.Report, error) {
	t.Helper()
	return ld.ImportFile(context.Background(), "data.csv", strings.NewReader(csv), opts)
}

func TestNew_RejectsBadConfig(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*loader.Config)
		wantErr string
	}{
		{"no table", func(c *loader.Config) { c.Table = " " }, "Table is required"},
		{"no columns", func(c *loader.Config) { c.Columns = nil }, "at least one column"},
		{"unnamed column", func(c *loader.Config) { c.Columns = []loader.Column{{}} }, "no Name"},
		{"duplicate column", func(c *loader.Config) {
			c.Columns = []loader.Column{{Name: "a"}, {Name: "A"}}
			c.Key = nil
		}, "listed twice"},
		{"unknown type", func(c *loader.Config) { c.Columns = []loader.Column{{Name: "a", Type: 99}}; c.Key = nil }, "unknown Type"},
		{"range on string", func(c *loader.Config) {
			c.Columns = []loader.Column{{Name: "a", Range: &loader.Range{}}}
			c.Key = nil
		}, "Range"},
		{"default on required", func(c *loader.Config) {
			c.Columns = []loader.Column{{Name: "a", Required: true, Default: "x"}}
			c.Key = nil
		}, "Default"},
		{"key not a column", func(c *loader.Config) { c.Key = []string{"id"} }, "not in Columns"},
		{"key not required", func(c *loader.Config) { c.Key = []string{"age"} }, "must be Required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loader.Config{Table: "customers", Columns: testColumns(), Key: []string{"email"}}
			tt.mutate(&cfg)
			_, err := loader.New(cfg)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestImportFile_NormalisesConvertsAndDefaults(t *testing.T) {
	db := &fakeDB{}
	ld := newTestLoader(t, db, nil)

	report, err := importCSV(t, ld,
		"Name, EMAIL ,age,country,plan\n"+
			" Alice ,A@Example.COM, 34 , us , PRO \n"+
			"Bob,b@example.com,,,\n", loader.Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !report.Completed || report.RowsRead != 2 || report.RowsWritten != 2 {
		t.Errorf("report = %+v", report)
	}
	rows := db.Rows()
	want := [][]any{
		{"a@example.com", "Alice", int64(34), "US", "pro"},
		{"b@example.com", "Bob", nil, nil, "free"},
	}
	for i := range want {
		for j := range want[i] {
			if rows[i][j] != want[i][j] {
				t.Errorf("row %d column %d = %#v, want %#v", i, j, rows[i][j], want[i][j])
			}
		}
	}
}

func TestImportFile_RowRules(t *testing.T) {
	tests := []struct {
		name       string
		row        string
		wantColumn string
		wantMsg    string
	}{
		{"valid", "a@example.com,Alice,30,US,pro", "", ""},
		{"age lower bound", "a@example.com,Alice,0,,", "", ""},
		{"age upper bound", "a@example.com,Alice,130,,", "", ""},
		{"missing email", ",Alice,,,", "email", "email is required"},
		{"bad email", "alice,Alice,,,", "email", "email is not a valid address"},
		{"email with display name", "Alice <a@example.com>,Alice,,,", "email", "not a valid address"},
		{"email too long", strings.Repeat("a", 250) + "@example.com,Alice,,,", "email", "longer than 254"},
		{"blank name", "a@example.com,   ,,,", "name", "name is required"},
		{"name too long", "a@example.com," + strings.Repeat("é", 11) + ",,,", "name", "longer than 10"},
		{"age not a number", "a@example.com,Alice,abc,,", "age", "age must be a whole number"},
		{"age decimal", "a@example.com,Alice,30.5,,", "age", "whole number"},
		{"age negative", "a@example.com,Alice,-1,,", "age", "between 0 and 130"},
		{"age too high", "a@example.com,Alice,131,,", "age", "between 0 and 130"},
		{"country pattern", "a@example.com,Alice,,USA,", "country", "country must be a two-letter code"},
		{"plan not allowed", "a@example.com,Alice,,,gold", "plan", "plan must be one of free, pro"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ld := newTestLoader(t, &fakeDB{}, nil)

			report, err := importCSV(t, ld, "email,name,age,country,plan\n"+tt.row+"\n", loader.Options{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantColumn == "" {
				if report.RowsRejected != 0 {
					t.Fatalf("row was rejected: %+v", report.Errors)
				}
				return
			}
			if len(report.Errors) != 1 {
				t.Fatalf("errors = %+v, want one", report.Errors)
			}
			issue := report.Errors[0]
			if issue.Row != 1 || issue.Column != tt.wantColumn || !strings.Contains(issue.Message, tt.wantMsg) {
				t.Errorf("issue = %+v, want column %q containing %q", issue, tt.wantColumn, tt.wantMsg)
			}
			if strings.Contains(issue.Message, "invalid record") {
				t.Errorf("message leaks the sentinel prefix: %q", issue.Message)
			}
		})
	}
}

func TestImportFile_ReportsEveryBadRowWithoutFailing(t *testing.T) {
	db := &fakeDB{}
	ld := newTestLoader(t, db, nil)

	report, err := importCSV(t, ld, "email,name\na@example.com,A\nbad,B\nc@example.com,\nd@example.com,D\n", loader.Options{})

	if err != nil {
		t.Fatalf("invalid rows must not fail the import: %v", err)
	}
	if !report.HasRejectedRows() || report.RowsRejected != 2 || len(report.Errors) != 2 {
		t.Fatalf("report = %+v, want both bad rows listed", report)
	}
	if report.Errors[0].Row != 2 || report.Errors[1].Row != 3 {
		t.Errorf("errors = %+v, want rows 2 and 3", report.Errors)
	}
	if report.RowsAccepted != 2 || report.RowsWritten != 2 || !report.Completed || len(db.Rows()) != 2 {
		t.Errorf("valid rows must still be written: report = %+v, stored = %d", report, len(db.Rows()))
	}
}

func TestImportFile_ValidFileHasNoRejections(t *testing.T) {
	db := &fakeDB{}
	ld := newTestLoader(t, db, nil)

	report, err := importCSV(t, ld, "email,name\na@example.com,A\nc@example.com,C\n", loader.Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.HasRejectedRows() || !report.Completed || len(db.Rows()) != 2 {
		t.Errorf("report = %+v, stored = %d", report, len(db.Rows()))
	}
}

func TestImportFile_DuplicateKeyInFile(t *testing.T) {
	ld := newTestLoader(t, &fakeDB{}, nil)

	report, err := importCSV(t, ld, "email,name\na@example.com,A\nA@Example.com,Again\nb@example.com,B\n", loader.Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.RowsAccepted != 2 || len(report.Errors) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if issue := report.Errors[0]; issue.Row != 2 || !strings.Contains(issue.Message, "already appears on row 1") {
		t.Errorf("issue = %+v", issue)
	}
}

func TestImportFile_RejectedRowDoesNotReserveItsKey(t *testing.T) {
	ld := newTestLoader(t, &fakeDB{}, nil)

	report, err := importCSV(t, ld, "email,name\na@example.com,\na@example.com,Alice\n", loader.Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.RowsAccepted != 1 || report.RowsRejected != 1 {
		t.Errorf("report = %+v, want the second row accepted", report)
	}
}

func TestImportFile_CompositeKey(t *testing.T) {
	db := &fakeDB{}
	ld := newTestLoader(t, db, func(c *loader.Config) {
		c.Columns = []loader.Column{
			{Name: "org", Required: true},
			{Name: "user", Required: true},
		}
		c.Key = []string{"org", "user"}
	})

	report, err := importCSV(t, ld, "org,user\nx,1\nx,2\ny,1\nx,1\n", loader.Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.RowsAccepted != 3 || report.RowsRejected != 1 || report.Errors[0].Row != 4 {
		t.Errorf("report = %+v", report)
	}
}

func TestImportFile_UsesUpsertOnlyWithAKey(t *testing.T) {
	keyed := &fakeDB{}
	if _, err := importCSV(t, newTestLoader(t, keyed, nil), "email,name\na@example.com,A\n", loader.Options{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(keyed.Statements(), ";"), "ON CONFLICT") {
		t.Errorf("statements = %q, want an upsert", keyed.Statements())
	}

	plain := &fakeDB{}
	ld := newTestLoader(t, plain, func(c *loader.Config) { c.Key = nil })
	if _, err := importCSV(t, ld, "email,name\na@example.com,A\n", loader.Options{}); err != nil {
		t.Fatal(err)
	}
	if len(plain.Statements()) != 0 || len(plain.Rows()) != 1 {
		t.Errorf("statements = %q, rows = %d, want a plain COPY", plain.Statements(), len(plain.Rows()))
	}
}

func TestImportFile_BatchesAreFlushed(t *testing.T) {
	db := &fakeDB{}
	ld := newTestLoader(t, db, func(c *loader.Config) { c.BatchSize = 2 })

	report, err := importCSV(t, ld, "email,name\na@x.io,A\nb@x.io,B\nc@x.io,C\n", loader.Options{})
	if err != nil || report.RowsWritten != 3 || len(db.Rows()) != 3 {
		t.Errorf("report = %+v, err = %v, stored = %d", report, err, len(db.Rows()))
	}
}

func TestImportFile_HeaderProblems(t *testing.T) {
	tests := []struct {
		name    string
		csv     string
		wantMsg string
	}{
		{"missing required", "email,age\na@example.com,3\n", "missing required columns: name"},
		{"missing both", "age\n3\n", "missing required columns: email, name"},
		{"duplicate column", "email,name,EMAIL\na@example.com,A,b@example.com\n", `duplicate column "email"`},
		{"empty file", "", "empty"},
		{"ragged csv", "email,name\na@example.com\n", "malformed CSV"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ld := newTestLoader(t, &fakeDB{}, nil)

			_, err := importCSV(t, ld, tt.csv, loader.Options{})

			var inputErr *loader.InputError
			if !errors.As(err, &inputErr) || !strings.Contains(inputErr.Message, tt.wantMsg) {
				t.Errorf("err = %v, want an *InputError containing %q", err, tt.wantMsg)
			}
		})
	}
}

func TestImportFile_HeaderOnlyIsAnEmptyImport(t *testing.T) {
	ld := newTestLoader(t, &fakeDB{}, nil)

	report, err := importCSV(t, ld, "email,name\n", loader.Options{})
	if err != nil || !report.Completed || report.RowsRead != 0 {
		t.Errorf("report = %+v, err = %v", report, err)
	}
}

func TestImportFile_UnsupportedExtension(t *testing.T) {
	ld := newTestLoader(t, &fakeDB{}, nil)

	for _, filename := range []string{"data.txt", "data", ""} {
		_, err := ld.ImportFile(context.Background(), filename, strings.NewReader("x"), loader.Options{})

		if !errors.Is(err, loader.ErrUnsupportedFormat) {
			t.Errorf("%q: err = %v, want ErrUnsupportedFormat", filename, err)
		}
	}
}

func TestImportFile_ExtensionIsCaseInsensitive(t *testing.T) {
	ld := newTestLoader(t, &fakeDB{}, nil)

	report, err := ld.ImportFile(context.Background(), "DATA.CSV", strings.NewReader("email,name\na@example.com,A\n"), loader.Options{})

	if err != nil || report.RowsWritten != 1 {
		t.Errorf("report = %+v, err = %v", report, err)
	}
}

func TestImportFile_CorruptWorkbookIsInputError(t *testing.T) {
	ld := newTestLoader(t, &fakeDB{}, nil)

	_, err := ld.ImportFile(context.Background(), "data.xlsx", strings.NewReader("not a zip"), loader.Options{})

	var inputErr *loader.InputError
	if !errors.As(err, &inputErr) {
		t.Errorf("err = %v, want an *InputError", err)
	}
}

func TestImportFile_ReaderErrorsStayReachable(t *testing.T) {
	boom := errors.New("connection reset")
	ld := newTestLoader(t, &fakeDB{}, nil)
	reader := io.MultiReader(strings.NewReader("email,name\na@example.com,A\n"), errReader{boom})

	_, err := ld.ImportFile(context.Background(), "data.csv", reader, loader.Options{})

	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap %v", err, boom)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestImportFile_ReportIsCapped(t *testing.T) {
	ld := newTestLoader(t, &fakeDB{}, func(c *loader.Config) { c.MaxReportedIssues = 3 })

	report, err := importCSV(t, ld, "email,name\nbad,A\nbad,B\nbad,C\nbad,D\nbad,E\n", loader.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Errors) != 3 || report.ErrorsNotShown != 2 || report.RowsRejected != 5 {
		t.Errorf("shown = %d, not shown = %d, rejected = %d", len(report.Errors), report.ErrorsNotShown, report.RowsRejected)
	}
}

func TestImportFile_DatabaseFailureIsNotAnInputError(t *testing.T) {
	db := &fakeDB{CopyErr: errors.New("password authentication failed for user admin")}
	ld := newTestLoader(t, db, nil)

	report, err := importCSV(t, ld, "email,name\na@example.com,A\n", loader.Options{})

	var inputErr *loader.InputError
	if err == nil || errors.As(err, &inputErr) || errors.Is(err, loader.ErrUnsupportedFormat) {
		t.Errorf("err = %v, want a plain server-side error", err)
	}
	if report.Completed {
		t.Error("a failed import must not report completion")
	}
}

func TestColumnTypes(t *testing.T) {
	db := &fakeDB{}
	ld := newTestLoader(t, db, func(c *loader.Config) {
		c.Columns = []loader.Column{
			{Name: "id", Required: true},
			{Name: "price", Type: loader.Float},
			{Name: "active", Type: loader.Bool},
		}
		c.Key = []string{"id"}
	})

	report, err := importCSV(t, ld, "id,price,active\n1,9.5,true\n2,abc,true\n3,1,maybe\n", loader.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.RowsAccepted != 1 || len(report.Errors) != 2 {
		t.Fatalf("report = %+v", report)
	}
	if rows := db.Rows(); rows[0][1] != 9.5 || rows[0][2] != true {
		t.Errorf("row = %#v", rows[0])
	}
	if !strings.Contains(report.Errors[0].Message, "must be a number") || !strings.Contains(report.Errors[1].Message, "must be true or false") {
		t.Errorf("errors = %+v", report.Errors)
	}
}
