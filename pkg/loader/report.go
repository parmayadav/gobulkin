package loader

import (
	"errors"
	"strings"

	"github.com/parmayadav/gobulkin/pkg/core"
)

// RowIssue describes one rejected row.
type RowIssue struct {
	Row     int    `json:"row"`
	Column  string `json:"column,omitempty"`
	Message string `json:"message"`
}

// Report is the outcome of an import and marshals to JSON as is.
type Report struct {
	// Completed means the import ran to the end without error. Written rows are durable
	// only once the caller commits.
	Completed    bool  `json:"completed"`
	RowsRead     int64 `json:"rows_read"`
	RowsAccepted int64 `json:"rows_accepted"`
	RowsRejected int64 `json:"rows_rejected"`
	// RowsWritten is zero unless Completed.
	RowsWritten int64 `json:"rows_written"`
	// Errors lists the first rejected rows, up to Config.MaxReportedIssues.
	Errors         []RowIssue `json:"errors,omitempty"`
	ErrorsNotShown int        `json:"errors_not_shown,omitempty"`
}

// HasRejectedRows reports whether any row failed validation.
func (r *Report) HasRejectedRows() bool { return r.RowsRejected > 0 }

func (r *rules) buildReport(result core.Result, err error) *Report {
	report := &Report{
		Completed:      err == nil,
		RowsRead:       result.RowsRead,
		RowsAccepted:   result.RowsAccepted,
		RowsRejected:   result.RowsRejected,
		Errors:         r.issues.items,
		ErrorsNotShown: r.issues.dropped,
	}
	if err == nil {
		report.RowsWritten = result.RowsWritten
	}
	return report
}

// issueLog keeps the first max issues.
type issueLog struct {
	max     int
	items   []RowIssue
	dropped int
}

func (l *issueLog) add(row int, err error) {
	if len(l.items) >= l.max {
		l.dropped++
		return
	}

	issue := RowIssue{Row: row, Message: err.Error()}
	var rowErr *core.RowError
	if errors.As(err, &rowErr) {
		issue.Column = rowErr.Column
		issue.Message = rowErr.Err.Error()
	}
	issue.Message = strings.TrimPrefix(issue.Message, core.ErrInvalidRecord.Error()+": ")
	l.items = append(l.items, issue)
}
