package core

// ErrorPolicy decides what happens when a hook rejects a row.
type ErrorPolicy int

const (
	// StopOnError ends the import at the first rejected row.
	StopOnError ErrorPolicy = iota
	// SkipInvalidRows counts rejected rows and keeps going.
	SkipInvalidRows
)

// Options configure Import.
type Options struct {
	// BatchSize is how many rows are buffered per write; DefaultBatchSize when not positive.
	BatchSize   int
	ErrorPolicy ErrorPolicy
	Hooks       Hooks
}
