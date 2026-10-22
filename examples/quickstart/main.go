// Command quickstart imports a CSV or XLSX file into PostgreSQL and prints the report as JSON.
//
//	DATABASE_URL=postgres://... go run ./examples/quickstart -file examples/quickstart/customers.csv
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/parmayadav/gobulkin/pkg/loader"
)

const createTable = `CREATE TABLE IF NOT EXISTS customers (
	email   text PRIMARY KEY,
	name    text NOT NULL,
	age     integer,
	country text,
	plan    text NOT NULL DEFAULT 'free'
)`

type result struct {
	*loader.Report
	Committed bool `json:"committed"`
}

func main() {
	path := flag.String("file", "examples/quickstart/customers.csv", "CSV or XLSX file to import")
	partial := flag.Bool("partial", false, "commit the valid rows even if some rows are rejected")
	flag.Parse()

	if err := run(*path, *partial); err != nil {
		log.Fatal(err)
	}
}

func run(path string, partial bool) error {
	ctx := context.Background()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL must be set")
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, createTable); err != nil {
		return err
	}

	customers, err := loader.New(loader.Config{
		Table: "customers",
		Key:   []string{"email"},
		Columns: []loader.Column{
			{Name: "email", Required: true, Case: loader.Lower, Check: loader.Email},
			{Name: "name", Required: true, MaxLength: 100},
			{Name: "age", Type: loader.Int, Range: &loader.Range{Min: 0, Max: 130}},
			{Name: "country", Case: loader.Upper, Pattern: regexp.MustCompile(`^[A-Z]{2}$`), PatternMessage: "must be a two-letter ISO code"},
			{Name: "plan", Case: loader.Lower, OneOf: []string{"free", "pro", "enterprise"}, Default: "free"},
		},
	})
	if err != nil {
		return err
	}

	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // no-op after Commit

	report, err := customers.ImportFile(ctx, tx, filepath.Base(path), file, loader.Options{})
	if err != nil {
		return err
	}

	committed := !report.HasRejectedRows() || partial
	if committed {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}

	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	return out.Encode(result{Report: report, Committed: committed})
}
