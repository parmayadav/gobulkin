// Package core streams rows from a Source through validation hooks and hands them to a
// writer in bounded batches. It has no file-format or database dependencies.
//
// Any Source can feed Import and any WriteFunc can receive its batches. Most callers want
// pkg/loader, which builds on this package and pkg/postgres.
package core
