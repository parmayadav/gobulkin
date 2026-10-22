package loader

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/parmayadav/gobulkin/pkg/core"
)

const (
	maxEmailLength = 254
	keySeparator   = "\x1f"
)

// Email is a Check that accepts a bare address and rejects the "Name <a@b.c>" form.
func Email(value string) error {
	if len(value) > maxEmailLength {
		return fmt.Errorf("is longer than %d characters", maxEmailLength)
	}
	// ParseAddress accepts display names; the round trip rejects them.
	if addr, err := mail.ParseAddress(value); err != nil || addr.Address != value {
		return errors.New("is not a valid address")
	}
	return nil
}

// rules is the per-file state of a column spec.
type rules struct {
	columns []Column
	key     []string
	seen    map[string]int
	issues  *issueLog
}

func newRules(columns []Column, key []string, header *core.Header, maxIssues int) (*rules, error) {
	seen := make(map[string]struct{})
	for _, name := range header.Names() {
		if name == "" {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, &InputError{Message: fmt.Sprintf("duplicate column %q", name)}
		}
		seen[name] = struct{}{}
	}

	var required []string
	for _, column := range columns {
		if column.Required {
			required = append(required, column.Name)
		}
	}
	if missing := header.Missing(required...); len(missing) > 0 {
		return nil, &InputError{Message: "missing required columns: " + strings.Join(missing, ", ")}
	}

	return &rules{
		columns: columns,
		key:     key,
		seen:    make(map[string]int),
		issues:  &issueLog{max: maxIssues},
	}, nil
}

func (r *rules) hooks() core.Hooks {
	return core.Hooks{
		BeforeRow:   r.normalize,
		ValidateRow: r.validate,
		AfterRow:    r.remember,
		OnRowError:  r.report,
	}
}

func (r *rules) normalize(_ context.Context, rec *core.Record) error {
	for _, column := range r.columns {
		value := strings.TrimSpace(rec.Value(column.Name))
		switch column.Case {
		case Lower:
			value = strings.ToLower(value)
		case Upper:
			value = strings.ToUpper(value)
		}
		rec.Set(column.Name, value)
	}
	return nil
}

func (r *rules) validate(_ context.Context, rec *core.Record) error {
	for _, column := range r.columns {
		if err := column.check(rec.Value(column.Name)); err != nil {
			return err
		}
	}
	return r.checkKey(*rec)
}

// remember runs for accepted rows only, so a rejected row does not block a later duplicate.
func (r *rules) remember(_ context.Context, rec core.Record) error {
	if len(r.key) > 0 {
		r.seen[r.keyOf(rec)] = rec.Number
	}
	return nil
}

func (r *rules) report(_ context.Context, rec core.Record, err error) error {
	r.issues.add(rec.Number, err)
	return nil
}

func (r *rules) checkKey(rec core.Record) error {
	if len(r.key) == 0 {
		return nil
	}
	if first, duplicate := r.seen[r.keyOf(rec)]; duplicate {
		reason := "already appears on row %d"
		if len(r.key) > 1 {
			reason = "is part of a key (" + strings.Join(r.key, "+") + ") that already appears on row %d"
		}
		return invalid(r.key[0], rec.Value(r.key[0]), reason, first)
	}
	return nil
}

func (r *rules) keyOf(rec core.Record) string {
	parts := make([]string, len(r.key))
	for i, name := range r.key {
		parts[i] = rec.Value(name)
	}
	return strings.Join(parts, keySeparator)
}

// convert maps a validated record to one table row.
func (r *rules) convert(rec core.Record) ([]any, error) {
	row := make([]any, len(r.columns))
	for i, column := range r.columns {
		value, err := column.convert(rec.Value(column.Name))
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", column.Name, err)
		}
		row[i] = value
	}
	return row, nil
}

func (c Column) check(value string) error {
	if value == "" {
		if c.Required {
			return invalid(c.Name, value, "is required")
		}
		return nil
	}

	checks := []func(string) string{
		c.checkLength, c.checkType, c.checkRange, c.checkOneOf, c.checkPattern, c.checkCustom,
	}
	for _, check := range checks {
		if reason := check(value); reason != "" {
			return invalid(c.Name, value, "%s", reason)
		}
	}
	return nil
}

// The check methods return the reason a value fails, or "".

func (c Column) checkLength(value string) string {
	if c.MaxLength > 0 && utf8.RuneCountInString(value) > c.MaxLength {
		return fmt.Sprintf("is longer than %d characters", c.MaxLength)
	}
	return ""
}

func (c Column) checkType(value string) string {
	_, err := c.convert(value)
	if err == nil {
		return ""
	}
	switch c.Type {
	case Int:
		return "must be a whole number"
	case Float:
		return "must be a number"
	case Bool:
		return "must be true or false"
	default:
		return ""
	}
}

func (c Column) checkRange(value string) string {
	if c.Range == nil {
		return ""
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return ""
	}
	if number < c.Range.Min || number > c.Range.Max {
		return fmt.Sprintf("must be between %v and %v", c.Range.Min, c.Range.Max)
	}
	return ""
}

func (c Column) checkOneOf(value string) string {
	if len(c.OneOf) == 0 {
		return ""
	}
	for _, allowed := range c.OneOf {
		if value == allowed {
			return ""
		}
	}
	return "must be one of " + strings.Join(c.OneOf, ", ")
}

func (c Column) checkPattern(value string) string {
	if c.Pattern == nil || c.Pattern.MatchString(value) {
		return ""
	}
	if c.PatternMessage != "" {
		return c.PatternMessage
	}
	return "has an invalid format"
}

func (c Column) checkCustom(value string) string {
	if c.Check == nil {
		return ""
	}
	if err := c.Check(value); err != nil {
		return err.Error()
	}
	return ""
}

// convert turns validated text into the value stored in the table.
func (c Column) convert(value string) (any, error) {
	if value == "" {
		return c.Default, nil
	}
	switch c.Type {
	case Int:
		return strconv.ParseInt(value, 10, 64)
	case Float:
		return strconv.ParseFloat(value, 64)
	case Bool:
		return strconv.ParseBool(value)
	default:
		return value, nil
	}
}

// invalid builds a row rejection reading "<column> <reason>".
func invalid(column, value, format string, args ...any) error {
	reason := fmt.Sprintf(format, args...)
	return &core.RowError{
		Column: column,
		Value:  value,
		Err:    fmt.Errorf("%w: %s %s", core.ErrInvalidRecord, column, reason),
	}
}
