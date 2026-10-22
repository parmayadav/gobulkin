package main

import (
	"regexp"

	"github.com/parmayadav/gobulkin/pkg/loader"
)

const (
	maxNameLength = 100
	maxAge        = 130
	defaultPlan   = "free"
)

var countryCode = regexp.MustCompile(`^[A-Z]{2}$`)

// newCustomerLoader declares the shape of a customers upload.
func newCustomerLoader() (*loader.Loader, error) {
	return loader.New(loader.Config{
		Table: "customers",
		// Re-uploading a file updates existing customers.
		Key: []string{"email"},
		Columns: []loader.Column{
			{Name: "email", Required: true, Case: loader.Lower, Check: loader.Email},
			{Name: "name", Required: true, MaxLength: maxNameLength},
			{Name: "age", Type: loader.Int, Range: &loader.Range{Min: 0, Max: maxAge}},
			{Name: "country", Case: loader.Upper, Pattern: countryCode, PatternMessage: "must be a two-letter ISO code"},
			{Name: "plan", Case: loader.Lower, OneOf: []string{"free", "pro", "enterprise"}, Default: defaultPlan},
		},
	})
}
