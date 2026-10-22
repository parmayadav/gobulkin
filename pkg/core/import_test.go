package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
)

type sliceSource struct {
	headers []string
	records []Record
	pos     int
	closed  bool
}

func newSliceSource(rows ...[]string) *sliceSource {
	return newHeaderSource(nil, rows...)
}

func newHeaderSource(headers []string, rows ...[]string) *sliceSource {
	src := &sliceSource{headers: headers}
	for i, values := range rows {
		src.records = append(src.records, Record{Number: i + 1, Values: values})
	}
	return src
}

func (s *sliceSource) Headers() []string { return s.headers }

func (s *sliceSource) Close() error {
	s.closed = true
	return nil
}

func (s *sliceSource) Next(ctx context.Context) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if s.pos >= len(s.records) {
		return Record{}, io.EOF
	}
	rec := s.records[s.pos]
	s.pos++
	return rec, nil
}

type writeRecorder struct {
	batches [][][]any
	err     error
}

func (w *writeRecorder) write(_ context.Context, rows [][]any) (int64, error) {
	if w.err != nil {
		return 0, w.err
	}
	w.batches = append(w.batches, rows)
	return int64(len(rows)), nil
}

func (w *writeRecorder) sizes() []int {
	var sizes []int
	for _, batch := range w.batches {
		sizes = append(sizes, len(batch))
	}
	return sizes
}

func firstValue(rec Record) ([]any, error) {
	return []any{rec.Values[0]}, nil
}

func rejectValue(bad string) RowHook {
	return func(_ context.Context, rec *Record) error {
		if rec.Values[0] == bad {
			return &RowError{
				Column: "v",
				Value:  bad,
				Err:    fmt.Errorf("%w: bad value", ErrInvalidRecord),
			}
		}
		return nil
	}
}

func values(n int) [][]string {
	rows := make([][]string, n)
	for i := range rows {
		rows[i] = []string{fmt.Sprintf("v%d", i+1)}
	}
	return rows
}

func TestImport_BatchesAndTotals(t *testing.T) {
	rec := &writeRecorder{}
	res, err := Import(context.Background(), newSliceSource(values(5)...), rec.write, firstValue,
		Options{BatchSize: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := []int{2, 2, 1}; !reflect.DeepEqual(rec.sizes(), want) {
		t.Errorf("batch sizes = %v, want %v", rec.sizes(), want)
	}
	if want := (Result{RowsRead: 5, RowsAccepted: 5, RowsWritten: 5}); res != want {
		t.Errorf("result = %+v, want %+v", res, want)
	}
}

func TestImport_DefaultBatchSize(t *testing.T) {
	rec := &writeRecorder{}
	rows := values(DefaultBatchSize + 1)

	if _, err := Import(context.Background(), newSliceSource(rows...), rec.write, firstValue, Options{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []int{DefaultBatchSize, 1}; !reflect.DeepEqual(rec.sizes(), want) {
		t.Errorf("batch sizes = %v, want %v", rec.sizes(), want)
	}
}

func TestImport_RequiresArguments(t *testing.T) {
	rec := &writeRecorder{}
	src := newSliceSource(values(1)...)

	tests := []struct {
		name   string
		src    Source
		write  WriteFunc
		mapRow Mapper
	}{
		{"nil source", nil, rec.write, firstValue},
		{"nil write", src, nil, firstValue},
		{"nil mapper", src, rec.write, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Import(context.Background(), tt.src, tt.write, tt.mapRow, Options{})
			if err == nil {
				t.Fatal("expected an error")
			}
			if res != (Result{}) {
				t.Errorf("result = %+v, want zero", res)
			}
		})
	}
	if src.pos != 0 {
		t.Error("source must not be read when arguments are invalid")
	}
}

func TestImport_DoesNotCloseSource(t *testing.T) {
	src := newSliceSource(values(1)...)

	if _, err := Import(context.Background(), src, (&writeRecorder{}).write, firstValue, Options{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src.closed {
		t.Error("the caller owns the source; Import must not close it")
	}
}

func TestImport_EmptySourceWritesNothing(t *testing.T) {
	rec := &writeRecorder{}
	res, err := Import(context.Background(), newSliceSource(), rec.write, firstValue, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.batches) != 0 || res != (Result{}) {
		t.Errorf("batches = %v, result = %+v", rec.batches, res)
	}
}

func TestImport_SkipInvalidRows(t *testing.T) {
	var reported []*RowError
	opts := Options{
		ErrorPolicy: SkipInvalidRows,
		Hooks: Hooks{
			ValidateRow: rejectValue("v2"),
			OnRowError: func(_ context.Context, _ Record, err error) error {
				var rowErr *RowError
				if errors.As(err, &rowErr) {
					reported = append(reported, rowErr)
				}
				return nil
			},
		},
	}

	rec := &writeRecorder{}
	res, err := Import(context.Background(), newSliceSource(values(3)...), rec.write, firstValue, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := (Result{RowsRead: 3, RowsAccepted: 2, RowsRejected: 1, RowsWritten: 2}); res != want {
		t.Errorf("result = %+v, want %+v", res, want)
	}
	if len(reported) != 1 || reported[0].Row != 2 || reported[0].Column != "v" {
		t.Errorf("reported = %+v, want one error stamped with row 2", reported)
	}
}

func TestImport_StopOnErrorStopsBeforeWriting(t *testing.T) {
	opts := Options{
		BatchSize: 100,
		Hooks:     Hooks{ValidateRow: rejectValue("v2")},
	}

	rec := &writeRecorder{}
	res, err := Import(context.Background(), newSliceSource(values(3)...), rec.write, firstValue, opts)

	if !errors.Is(err, ErrImportStopped) {
		t.Fatalf("err = %v, want ErrImportStopped", err)
	}
	var rowErr *RowError
	if !errors.As(err, &rowErr) || rowErr.Row != 2 {
		t.Errorf("expected a RowError for row 2 in %v", err)
	}
	if len(rec.batches) != 0 {
		t.Errorf("wrote %d batches, want none", len(rec.batches))
	}
	if res.RowsRejected != 1 || res.RowsWritten != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestImport_PlainHookErrorIsStampedWithRow(t *testing.T) {
	cause := fmt.Errorf("%w: no column info", ErrInvalidRecord)
	opts := Options{
		ErrorPolicy: SkipInvalidRows,
		Hooks: Hooks{
			ValidateRow: func(context.Context, *Record) error { return cause },
			OnRowError: func(_ context.Context, _ Record, err error) error {
				var rowErr *RowError
				if !errors.As(err, &rowErr) || rowErr.Row != 1 || !errors.Is(rowErr, cause) {
					t.Errorf("OnRowError got %v, want a RowError for row 1 wrapping the cause", err)
				}
				return nil
			},
		},
	}

	if _, err := Import(context.Background(), newSliceSource(values(1)...), (&writeRecorder{}).write, firstValue, opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImport_NonValidationHookErrorAlwaysAborts(t *testing.T) {
	boom := errors.New("lookup service down")
	opts := Options{
		ErrorPolicy: SkipInvalidRows,
		Hooks: Hooks{
			ValidateRow: func(context.Context, *Record) error { return boom },
		},
	}

	res, err := Import(context.Background(), newSliceSource(values(2)...), (&writeRecorder{}).write, firstValue, opts)

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
	if res.RowsRejected != 0 {
		t.Errorf("an infrastructure error must not count as a rejected row: %+v", res)
	}
}

func TestImport_OnRowErrorCanAbort(t *testing.T) {
	boom := errors.New("too many errors")
	opts := Options{
		ErrorPolicy: SkipInvalidRows,
		Hooks: Hooks{
			ValidateRow: rejectValue("v1"),
			OnRowError:  func(context.Context, Record, error) error { return boom },
		},
	}

	_, err := Import(context.Background(), newSliceSource(values(2)...), (&writeRecorder{}).write, firstValue, opts)

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestImport_SkipsBlankRows(t *testing.T) {
	rows := [][]string{{"a"}, {""}, {"  ", ""}, {"b"}}
	rec := &writeRecorder{}

	res, err := Import(context.Background(), newSliceSource(rows...), rec.write, firstValue, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RowsRead != 2 || res.RowsWritten != 2 {
		t.Errorf("result = %+v, want 2 rows read and written", res)
	}
}

func TestImport_BeforeRowChangesAreMapped(t *testing.T) {
	opts := Options{Hooks: Hooks{
		BeforeRow: func(_ context.Context, rec *Record) error {
			rec.Values[0] = "changed"
			return nil
		},
	}}

	rec := &writeRecorder{}
	if _, err := Import(context.Background(), newSliceSource(values(1)...), rec.write, firstValue, opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := rec.batches[0][0][0]; got != "changed" {
		t.Errorf("mapped value = %v, want changed", got)
	}
}

func TestImport_BeforeImportErrorSkipsReading(t *testing.T) {
	boom := errors.New("not allowed")
	var afterErr error
	src := newSliceSource(values(2)...)
	opts := Options{Hooks: Hooks{
		BeforeImport: func(context.Context) error { return boom },
		AfterImport: func(_ context.Context, _ Result, err error) error {
			afterErr = err
			return nil
		},
	}}

	_, err := Import(context.Background(), src, (&writeRecorder{}).write, firstValue, opts)

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if src.pos != 0 {
		t.Errorf("source was read %d times, want 0", src.pos)
	}
	if !errors.Is(afterErr, boom) {
		t.Errorf("AfterImport saw %v, want the run error", afterErr)
	}
}

func TestImport_AfterImportReceivesResult(t *testing.T) {
	var got Result
	opts := Options{Hooks: Hooks{
		AfterImport: func(_ context.Context, res Result, _ error) error {
			got = res
			return nil
		},
	}}

	res, err := Import(context.Background(), newSliceSource(values(3)...), (&writeRecorder{}).write, firstValue, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != res {
		t.Errorf("AfterImport saw %+v, Import returned %+v", got, res)
	}
}

func TestImport_AfterImportError(t *testing.T) {
	hookErr := errors.New("audit failed")
	runErr := errors.New("write broke")
	opts := Options{Hooks: Hooks{
		AfterImport: func(context.Context, Result, error) error { return hookErr },
	}}

	t.Run("returned when the import succeeded", func(t *testing.T) {
		_, err := Import(context.Background(), newSliceSource(values(1)...), (&writeRecorder{}).write, firstValue, opts)
		if !errors.Is(err, hookErr) {
			t.Errorf("err = %v, want %v", err, hookErr)
		}
	})

	t.Run("does not mask the import error", func(t *testing.T) {
		rec := &writeRecorder{err: runErr}
		_, err := Import(context.Background(), newSliceSource(values(1)...), rec.write, firstValue, opts)
		if !errors.Is(err, runErr) || errors.Is(err, hookErr) {
			t.Errorf("err = %v, want only %v", err, runErr)
		}
	})
}

func TestImport_WriteErrorPropagates(t *testing.T) {
	boom := errors.New("disk full")
	rec := &writeRecorder{err: boom}

	_, err := Import(context.Background(), newSliceSource(values(3)...), rec.write, firstValue, Options{BatchSize: 2})

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestImport_MapperErrorAborts(t *testing.T) {
	boom := errors.New("cannot map")
	mapRow := func(Record) ([]any, error) { return nil, boom }

	_, err := Import(context.Background(), newSliceSource(values(1)...), (&writeRecorder{}).write, mapRow, Options{})

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestImport_SourceErrorAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Import(ctx, newSliceSource(values(1)...), (&writeRecorder{}).write, firstValue, Options{})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestImport_AfterRowRunsForAcceptedRowsOnly(t *testing.T) {
	var seen []int
	opts := Options{
		ErrorPolicy: SkipInvalidRows,
		Hooks: Hooks{
			ValidateRow: rejectValue("v2"),
			AfterRow: func(_ context.Context, rec Record) error {
				seen = append(seen, rec.Number)
				return nil
			},
		},
	}

	if _, err := Import(context.Background(), newSliceSource(values(3)...), (&writeRecorder{}).write, firstValue, opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []int{1, 3}; !reflect.DeepEqual(seen, want) {
		t.Errorf("AfterRow saw %v, want %v", seen, want)
	}
}
