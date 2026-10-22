package core

// Record is one data row from a Source.
type Record struct {
	// Number counts data rows from 1, not counting the header row.
	Number int
	Values []string

	// Header is set by Import so hooks and mappers can address cells by column name.
	// It is nil for records built by hand; Value and Set then find no columns.
	Header *Header
}

// Value returns the cell in column, or "" when the column or the cell is absent.
func (r Record) Value(column string) string {
	i, ok := r.Header.Index(column)
	if !ok || i >= len(r.Values) {
		return ""
	}
	return r.Values[i]
}

// Set stores value in column, padding short rows. It reports false if the column does not exist.
func (r *Record) Set(column, value string) bool {
	i, ok := r.Header.Index(column)
	if !ok {
		return false
	}
	for len(r.Values) <= i {
		r.Values = append(r.Values, "")
	}
	r.Values[i] = value
	return true
}

// Result totals what an import did. RowsWritten is what the WriteFunc reported; it is not
// durable until the caller commits.
type Result struct {
	RowsRead     int64
	RowsAccepted int64
	RowsRejected int64
	RowsWritten  int64
}
