package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// DefaultBatchSize is used when Options.BatchSize is not positive.
const DefaultBatchSize = 1000

// WriteFunc stores one non-empty batch of rows and returns how many it stored.
type WriteFunc func(ctx context.Context, rows [][]any) (int64, error)

// Mapper converts an accepted record into the column values of one row.
type Mapper func(rec Record) ([]any, error)

// Import streams src through opts.Hooks, maps accepted records with mapRow and passes them
// to write in batches. The caller owns and closes src.
//
// A hook error wrapping ErrInvalidRecord rejects the row; any other hook error aborts.
// With StopOnError the first rejected row ends the import with ErrImportStopped; with
// SkipInvalidRows it is counted and the import continues. Blank rows are skipped.
//
// Import opens no transaction; write decides what "all or nothing" means.
func Import(
	ctx context.Context,
	src Source,
	write WriteFunc,
	mapRow Mapper,
	opts Options,
) (Result, error) {
	if src == nil || write == nil || mapRow == nil {
		return Result{}, errors.New("gobulkin: source, write function and mapper are required")
	}

	imp := &importer{
		src:       src,
		write:     write,
		mapRow:    mapRow,
		opts:      opts,
		batchSize: opts.BatchSize,
		header:    NewHeader(src.Headers()),
	}
	if imp.batchSize <= 0 {
		imp.batchSize = DefaultBatchSize
	}

	err := imp.run(ctx)

	// A failing AfterImport hook must not mask the import's error.
	if hook := opts.Hooks.AfterImport; hook != nil {
		if hookErr := hook(ctx, imp.res, err); err == nil {
			err = hookErr
		}
	}
	return imp.res, err
}

type importer struct {
	src       Source
	write     WriteFunc
	mapRow    Mapper
	opts      Options
	batchSize int
	header    *Header
	batch     [][]any
	res       Result
}

func (imp *importer) run(ctx context.Context) error {
	if hook := imp.opts.Hooks.BeforeImport; hook != nil {
		if err := hook(ctx); err != nil {
			return err
		}
	}

	for {
		rec, err := imp.src.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read row: %w", err)
		}
		if isBlank(rec) {
			continue
		}
		rec.Header = imp.header
		if err := imp.process(ctx, rec); err != nil {
			return err
		}
	}
	return imp.flushBatch(ctx)
}

func (imp *importer) process(ctx context.Context, rec Record) error {
	imp.res.RowsRead++

	if err := imp.check(ctx, &rec); err != nil {
		if !errors.Is(err, ErrInvalidRecord) {
			return fmt.Errorf("hook failed on row %d: %w", rec.Number, err)
		}
		return imp.reject(ctx, rec, err)
	}

	row, err := imp.mapRow(rec)
	if err != nil {
		return fmt.Errorf("map row %d: %w", rec.Number, err)
	}
	imp.res.RowsAccepted++
	imp.batch = append(imp.batch, row)

	if hook := imp.opts.Hooks.AfterRow; hook != nil {
		if err := hook(ctx, rec); err != nil {
			return err
		}
	}
	if len(imp.batch) >= imp.batchSize {
		return imp.flushBatch(ctx)
	}
	return nil
}

func (imp *importer) check(ctx context.Context, rec *Record) error {
	for _, hook := range []RowHook{imp.opts.Hooks.BeforeRow, imp.opts.Hooks.ValidateRow} {
		if hook == nil {
			continue
		}
		if err := hook(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

func (imp *importer) reject(ctx context.Context, rec Record, cause error) error {
	imp.res.RowsRejected++
	rowErr := toRowError(rec, cause)

	if hook := imp.opts.Hooks.OnRowError; hook != nil {
		if err := hook(ctx, rec, rowErr); err != nil {
			return err
		}
	}
	if imp.opts.ErrorPolicy == StopOnError {
		return fmt.Errorf("%w: %w", ErrImportStopped, rowErr)
	}
	return nil
}

func (imp *importer) flushBatch(ctx context.Context) error {
	if len(imp.batch) == 0 {
		return nil
	}
	written, err := imp.write(ctx, imp.batch)
	if err != nil {
		return fmt.Errorf("write batch: %w", err)
	}
	imp.res.RowsWritten += written
	imp.batch = nil
	return nil
}

// toRowError stamps the hook's RowError, if any, with the record number.
func toRowError(rec Record, cause error) *RowError {
	var rowErr *RowError
	if errors.As(cause, &rowErr) {
		stamped := *rowErr
		stamped.Row = rec.Number
		return &stamped
	}
	return &RowError{Row: rec.Number, Err: cause}
}

func isBlank(rec Record) bool {
	for _, value := range rec.Values {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}
