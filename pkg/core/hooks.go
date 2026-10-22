package core

import (
	"context"
	"errors"
)

// RowHook inspects or edits a record before it is written. Return an error wrapping
// ErrInvalidRecord to reject the row; any other error aborts the import.
type RowHook func(context.Context, *Record) error

type ImportHook func(context.Context) error
type AfterRowHook func(context.Context, Record) error

// RowErrorHook is told about each rejected row; returning an error aborts the import.
type RowErrorHook func(context.Context, Record, error) error

// Hooks are optional callbacks. Per row they run in the order BeforeRow, ValidateRow,
// then AfterRow for accepted rows or OnRowError for rejected ones.
type Hooks struct {
	BeforeImport ImportHook
	BeforeRow    RowHook
	ValidateRow  RowHook
	AfterRow     AfterRowHook
	OnRowError   RowErrorHook
	// AfterImport always runs, with the import's error (nil on success).
	AfterImport func(context.Context, Result, error) error
}

var (
	// ErrInvalidRecord marks a row as rejected rather than the import as failed.
	ErrInvalidRecord = errors.New("invalid record")
	// ErrImportStopped wraps the first rejected row under StopOnError.
	ErrImportStopped = errors.New("import stopped")
)
