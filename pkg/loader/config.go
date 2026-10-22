// Package loader validates a CSV or XLSX file against a column spec and writes the valid
// rows to PostgreSQL through a caller-supplied transaction.
//
// Bad rows are reported, not fatal: the caller reads the Report and decides whether to
// commit or roll back. Multipart handling and the transaction itself are the caller's job.
package loader

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultTimeout           = 2 * time.Minute
	DefaultMaxReportedIssues = 100
)

// Type is how a column's text is converted before it is stored.
type Type int

const (
	String Type = iota
	Int
	Float
	Bool
)

// Case rewrites a column's text before it is validated.
type Case int

const (
	KeepCase Case = iota
	Lower
	Upper
)

// Range is an inclusive numeric limit.
type Range struct {
	Min, Max float64
}

// Column describes one column of both the file header and the table.
// Header names match ignoring case, spaces and a BOM, and values are trimmed.
//
// Rules run in order and stop at the first failure: Required, MaxLength, Type, Range,
// OneOf, Pattern, Check. An empty value skips every rule except Required.
type Column struct {
	Name string
	Type Type

	// Required also rejects a file whose header lacks the column.
	Required bool
	// Default replaces an empty value; without one the value is stored as NULL.
	Default any

	Case      Case
	MaxLength int // characters; zero means no limit
	Range     *Range
	OneOf     []string
	Pattern   *regexp.Regexp
	// PatternMessage and the error from Check complete the sentence "<name> ...".
	PatternMessage string
	Check          func(value string) error
}

type Config struct {
	// Table may be schema-qualified ("public.customers").
	Table   string
	Columns []Column
	// Key names a unique constraint. When set, existing rows are updated instead of failing
	// and a key repeated within the file is rejected. Key columns must be Required.
	Key []string

	BatchSize         int           // core.DefaultBatchSize when zero
	Timeout           time.Duration // DefaultTimeout when zero
	MaxReportedIssues int           // DefaultMaxReportedIssues when zero
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Table) == "" {
		return errors.New("loader: Table is required")
	}
	if len(c.Columns) == 0 {
		return errors.New("loader: at least one column is required")
	}

	required := make(map[string]bool, len(c.Columns))
	headerNames := make(map[string]struct{}, len(c.Columns))
	for _, column := range c.Columns {
		if err := column.validate(); err != nil {
			return err
		}
		// Headers match case-insensitively, so names differing only in case would collide.
		headerName := strings.ToLower(strings.TrimSpace(column.Name))
		if _, duplicate := headerNames[headerName]; duplicate {
			return fmt.Errorf("loader: column %q is listed twice", column.Name)
		}
		headerNames[headerName] = struct{}{}
		required[column.Name] = column.Required
	}

	for _, key := range c.Key {
		isRequired, known := required[key]
		if !known {
			return fmt.Errorf("loader: key column %q is not in Columns", key)
		}
		if !isRequired {
			return fmt.Errorf("loader: key column %q must be Required", key)
		}
	}
	return nil
}

func (c Column) validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("loader: a column has no Name")
	}
	if c.Type < String || c.Type > Bool {
		return fmt.Errorf("loader: column %q has an unknown Type", c.Name)
	}
	if c.Range != nil && c.Type != Int && c.Type != Float {
		return fmt.Errorf("loader: column %q has a Range but is not Int or Float", c.Name)
	}
	if c.Required && c.Default != nil {
		return fmt.Errorf("loader: column %q is Required, so its Default is never used", c.Name)
	}
	if c.MaxLength < 0 {
		return fmt.Errorf("loader: column %q has a negative MaxLength", c.Name)
	}
	return nil
}

// Options tune a single import.
type Options struct {
	// Sheet is the XLSX sheet to read; "Sheet1" when empty.
	Sheet string
}
