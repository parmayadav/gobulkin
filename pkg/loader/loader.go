package loader

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/parmayadav/gobulkin/pkg/core"
	"github.com/parmayadav/gobulkin/pkg/postgres"
)

// Tx is the subset of pgx.Tx the loader writes through. The caller begins, commits and
// rolls back.
type Tx interface {
	CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error)
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// Loader imports files into one table. It is safe for concurrent use.
type Loader struct {
	cfg         Config
	table       pgx.Identifier
	columnNames []string
}

// New validates cfg and returns a Loader.
func New(cfg Config) (*Loader, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxReportedIssues <= 0 {
		cfg.MaxReportedIssues = DefaultMaxReportedIssues
	}

	names := make([]string, len(cfg.Columns))
	for i, column := range cfg.Columns {
		names[i] = column.Name
	}
	return &Loader{
		cfg:         cfg,
		table:       pgx.Identifier(strings.Split(cfg.Table, ".")),
		columnNames: names,
	}, nil
}

// ImportFile validates every row of a CSV or XLSX file (format taken from the extension of
// filename, first row is the header) and writes the valid rows through tx.
//
// Invalid rows are skipped and listed in the Report; they do not make the call fail, so the
// caller can commit the valid rows or roll back and ask for a corrected file. Roll back
// whenever err is not nil, since earlier batches may already be written.
//
// Errors: ErrUnsupportedFormat, *InputError for a bad file or header, errors from content
// itself (reachable with errors.As), and anything else is a server-side failure.
// The Report is never nil.
func (l *Loader) ImportFile(ctx context.Context, tx Tx, filename string, content io.Reader, opts Options) (*Report, error) {
	ctx, cancel := context.WithTimeout(ctx, l.cfg.Timeout)
	defer cancel()

	src, err := openSource(filename, content, opts.Sheet)
	if err != nil {
		return &Report{}, openError(err)
	}
	defer src.Close()
	return l.run(ctx, tx, src, opts)
}

func (l *Loader) run(ctx context.Context, tx Tx, src core.Source, opts Options) (*Report, error) {
	rows, err := newRules(l.cfg.Columns, l.cfg.Key, core.NewHeader(src.Headers()), l.cfg.MaxReportedIssues)
	if err != nil {
		return &Report{}, err
	}

	importOpts := core.Options{
		BatchSize:   l.cfg.BatchSize,
		Hooks:       rows.hooks(),
		ErrorPolicy: core.SkipInvalidRows,
	}

	var result core.Result
	err = l.write(ctx, tx, func(write core.WriteFunc) error {
		var importErr error
		result, importErr = core.Import(ctx, src, write, rows.convert, importOpts)
		return importErr
	})

	// A malformed CSV row is a problem with the file, not the server.
	var parseErr *csv.ParseError
	if errors.As(err, &parseErr) {
		err = &InputError{Message: "malformed CSV: " + parseErr.Error(), Err: err}
	}
	return rows.buildReport(result, err), err
}

// write runs fn with a batch writer, then merges staged rows when a Key is set.
func (l *Loader) write(ctx context.Context, tx Tx, fn func(write core.WriteFunc) error) error {
	if len(l.cfg.Key) == 0 {
		return fn(postgres.NewWriter(tx).BatchFunc(l.table, l.columnNames))
	}

	upserter, err := postgres.NewUpserter(tx, postgres.UpsertSpec{
		Table:   l.table,
		Columns: l.columnNames,
		Key:     l.cfg.Key,
	})
	if err != nil {
		return err
	}
	if err := fn(upserter.Write); err != nil {
		return err
	}
	_, err = upserter.Merge(ctx)
	return err
}
