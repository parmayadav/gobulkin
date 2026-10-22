# gobulkin

A Go library for backend services that accept CSV or XLSX uploads. Describe your table once,
call one method per file, and gobulkin validates every row and bulk-loads the valid ones into
PostgreSQL through a transaction you control.

Receiving the upload (multipart, size limits, authentication) and the transaction itself stay
in your code. The library reads the file, checks the rows, writes through your transaction
and reports what it found.

## Features

- One call per file: `loader.ImportFile(ctx, tx, filename, reader, opts)`
- Declarative columns: required, type, length, range, allowed values, pattern, custom check
- CSV and XLSX, picked from the file extension, streamed in batches
- Every bad row is found in one pass and listed in a JSON-friendly report
- PostgreSQL COPY, or insert-or-update on a unique key, through your `pgx` transaction
- You decide whether to commit or roll back; the loader never does
- Lower-level `core` and `postgres` packages for custom sources, hooks and writers

## Layout

| Path | Purpose | Third-party deps |
| --- | --- | --- |
| `pkg/loader` | **Start here.** Column spec in, validated rows written to a transaction, report out | pgx, excelize |
| `pkg/core` | `Import`, hooks, `Record`, `Header`, `Source` | none |
| `pkg/postgres` | COPY `Writer` and `Upserter` | pgx |
| `examples/upload-api` | HTTP upload service built on `loader` | pgx |
| `docs` | Architecture notes | |

The CSV and XLSX readers are private to `loader`. `core` knows nothing about files or
databases, so it adds no dependencies to your build.

## Install

    go get github.com/parmayadav/gobulkin

Requires Go 1.25 or newer.

## Quick start

Declare the table once at start-up:

```go
import "github.com/parmayadav/gobulkin/pkg/loader"

customers, err := loader.New(loader.Config{
    Table: "customers",       // or "schema.customers"
    Key:   []string{"email"}, // optional: update existing rows instead of failing
    Columns: []loader.Column{
        {Name: "email", Required: true, Case: loader.Lower, Check: loader.Email},
        {Name: "name", Required: true, MaxLength: 100},
        {Name: "age", Type: loader.Int, Range: &loader.Range{Min: 0, Max: 130}},
        {Name: "plan", Case: loader.Lower, OneOf: []string{"free", "pro"}, Default: "free"},
    },
})
if err != nil {
    log.Fatal(err) // configuration mistakes surface here, not on the first upload
}
```

Then, for each upload, begin a transaction, import, and decide:

```go
tx, err := pool.Begin(ctx) // *pgxpool.Pool or *pgx.Conn
if err != nil {
    return err
}
defer tx.Rollback(context.WithoutCancel(ctx)) // no-op after Commit

report, err := customers.ImportFile(ctx, tx, "customers.csv", file, loader.Options{})
if err != nil {
    return err // nothing is committed
}
if report.HasRejectedRows() {
    // all-or-nothing: send report.Errors back so the file can be corrected
    return fmt.Errorf("%d rows rejected", report.RowsRejected)
}
return tx.Commit(ctx)
```

Skipping the `HasRejectedRows` check and committing anyway keeps the valid rows (partial
import). `examples/upload-api` shows the full HTTP version with multipart parsing, error
mapping and both policies.

## Using the loader

### Config

| Field | Meaning |
| --- | --- |
| `Table` | Target table, optionally schema-qualified |
| `Columns` | Column specs, see below; names are both the file header and the table column |
| `Key` | Unique-constraint columns. Enables insert-or-update and rejects a key repeated within the file. Key columns must be `Required` |
| `BatchSize` | Rows per database write (default 1000) |
| `Timeout` | Upper bound for one import (default 2 minutes) |
| `MaxReportedIssues` | Cap on `Report.Errors` (default 100) |

Without a `Key` rows are inserted with COPY, so a row that already exists fails the import. With
one, batches are staged in a temporary table and merged with `INSERT ... ON CONFLICT DO UPDATE`.

### Column rules

Rules run in this order and stop at the first failure for a row. Values are trimmed first, and
an empty value skips every rule except `Required`.

| Field | Effect |
| --- | --- |
| `Required` | An empty value rejects the row; a header without the column rejects the file |
| `Case` | `loader.Lower` or `loader.Upper` applied before validation |
| `MaxLength` | Longest allowed value, in characters |
| `Type` | `String` (default), `Int`, `Float` or `Bool`; the value is converted before saving |
| `Range` | Inclusive `Min` and `Max` for `Int` and `Float` |
| `OneOf` | Allowed values, compared after `Case` |
| `Pattern`, `PatternMessage` | Regular expression and the reason shown when it fails |
| `Check` | Your own `func(value string) error`; `loader.Email` is built in |
| `Default` | Saved in place of an empty value; otherwise an empty value is saved as NULL |

Header names match ignoring case, spaces and an Excel BOM. Extra columns in the file are ignored.
A column missing from the file counts as empty. A custom `Check` returns text that completes
"`<column> ...`", for example `errors.New("must start with SKU-")`.

### Options

`loader.Options{Sheet: "Q3"}` selects the XLSX sheet (`Sheet1` by default). It is ignored for CSV.

### Report

`ImportFile` always returns a `*Report`, which marshals to JSON as is:

| Field | Meaning |
| --- | --- |
| `completed` | The import ran to the end without an error |
| `rows_read`, `rows_accepted`, `rows_rejected` | Row counts |
| `rows_written` | Rows written to your transaction (durable only after you commit) |
| `errors` | The first rejected rows: `row`, `column`, `message` |
| `errors_not_shown` | How many more rejected rows were left out of `errors` |

`report.HasRejectedRows()` is true when any row failed validation. A bad row is never an
`error`; it only appears in the report.

### Errors

| Error | Meaning |
| --- | --- |
| `loader.ErrUnsupportedFormat` | The file name does not end in `.csv` or `.xlsx` |
| `*loader.InputError` | The file is empty, unreadable or malformed, or its header lacks required columns; `Message` is safe to show the uploader |
| errors from your reader | For example `*http.MaxBytesError`; reachable with `errors.As` (check before `*InputError`, which wraps them) |
| anything else | Server-side, such as the database: log it and return a generic message |

### Transactions

The loader only writes through the `loader.Tx` you pass (anything with `CopyFrom` and `Exec`;
`pgx.Tx` qualifies). It never begins, commits or rolls back. After `ImportFile` returns:

- `err != nil`: roll back, because earlier batches may already be written.
- `report.HasRejectedRows()`: your policy. Roll back for all-or-nothing, or commit to keep the
  valid rows.
- otherwise: commit.

Use one transaction per import. Beginning it before the body is read keeps a connection busy
while a slow client uploads, so read the file part first if that matters.

### Untrusted uploads

Cap the request body before importing (for HTTP, `http.MaxBytesReader`). An XLSX file is
buffered in memory, so keep the limit modest. The loader also caps the decompressed size of a
workbook at 256 MiB.

## How it works

```mermaid
flowchart LR
    A["Your handler: multipart, size limit, transaction"] --> B["loader: reader chosen by extension"]
    B --> C["core.Import: stream rows, run hooks"]
    C --> D["column rules: trim, case, validate, convert"]
    D --> E["postgres: COPY batches, merge if Key is set"]
    E --> F["Report back to your handler"]
    F --> G["Your handler: commit or roll back"]
```

```mermaid
sequenceDiagram
    participant App as Your handler
    participant L as loader
    participant DB as PostgreSQL

    App->>DB: Begin
    App->>L: ImportFile(tx, filename, reader)
    loop every BatchSize valid rows
        L->>DB: COPY batch (target table, or temp staging table with a Key)
    end
    opt Key is set
        L->>DB: INSERT ... SELECT FROM staging ON CONFLICT DO UPDATE
    end
    L-->>App: Report, error
    alt no error and rows acceptable
        App->>DB: Commit
    else
        App->>DB: Rollback
    end
```

## Lower-level API

Use `core` and `postgres` directly for custom validation across columns, other file formats
or other databases.

`core.Import` pulls rows from a `core.Source`, runs your hooks, buffers accepted rows and hands
each full batch to a `core.WriteFunc`, so memory use depends on the batch size, not the file.

```go
write := postgres.NewWriter(tx).BatchFunc(pgx.Identifier{"customers"}, []string{"email", "name"})
mapRow := func(rec core.Record) ([]any, error) {
    return []any{rec.Value("email"), rec.Value("name")}, nil
}

res, err := core.Import(ctx, src, write, mapRow, core.Options{
    ErrorPolicy: core.SkipInvalidRows, // default core.StopOnError ends at the first bad row
    Hooks: core.Hooks{
        ValidateRow: func(_ context.Context, rec *core.Record) error {
            if !strings.Contains(rec.Value("email"), "@") {
                return &core.RowError{
                    Column: "email",
                    Err:    fmt.Errorf("%w: not an email", core.ErrInvalidRecord),
                }
            }
            return nil
        },
    },
})
```

Hooks run per row as `BeforeRow`, `ValidateRow`, then `AfterRow` for accepted rows or
`OnRowError` for rejected ones; `BeforeImport` and `AfterImport` bracket the run. A hook error
wrapping `core.ErrInvalidRecord` rejects only that row, any other error aborts the import.
Cells are addressed by column name with `rec.Value("email")` and `rec.Set("email", v)`.

```mermaid
flowchart TD
    A["BeforeImport hook"] --> D{"src.Next"}
    D -- "io.EOF" --> L["Flush last batch"]
    D -- "read error" --> X["Abort"]
    D -- "row" --> E{"Blank row?"}
    E -- "yes" --> D
    E -- "no" --> G["BeforeRow, ValidateRow hooks"]
    G -- "other error" --> X
    G -- "wraps ErrInvalidRecord" --> R["Count rejected, OnRowError hook"]
    R --> P{"ErrorPolicy"}
    P -- "SkipInvalidRows" --> D
    P -- "StopOnError" --> S["Abort with ErrImportStopped"]
    G -- "ok" --> M["Mapper: Record to []any, AfterRow hook"]
    M --> Q{"Batch full?"}
    Q -- "no" --> D
    Q -- "yes" --> W["WriteFunc"]
    W --> D
    L --> Z["AfterImport hook"]
    X --> Z
    S --> Z
```

`core.Import` never begins or commits a transaction, and the rows counted in
`Result.RowsWritten` are durable only after you commit.

### Insert or update

`postgres.Upserter` stages batches in a temporary table and merges them once:

```go
up, err := postgres.NewUpserter(tx, postgres.UpsertSpec{
    Table:   pgx.Identifier{"customers"},
    Columns: []string{"email", "name"},
    Key:     []string{"email"},
})
if err != nil {
    return err
}
if _, err = core.Import(ctx, src, up.Write, mapRow, core.Options{}); err != nil {
    return err
}
_, err = up.Merge(ctx)
```

### Your own source

Any format works if it implements `core.Source`. `Next` returns an error wrapping `io.EOF` when
the rows run out:

```go
type sliceSource struct {
    headers []string
    rows    [][]string
    next    int
}

func (s *sliceSource) Headers() []string { return s.headers }

func (s *sliceSource) Next(ctx context.Context) (core.Record, error) {
    if err := ctx.Err(); err != nil {
        return core.Record{}, err
    }
    if s.next >= len(s.rows) {
        return core.Record{}, io.EOF
    }
    s.next++
    return core.Record{Number: s.next, Values: s.rows[s.next-1]}, nil
}

func (s *sliceSource) Close() error { return nil }
```

A `core.WriteFunc` is just a function, so you can also run an import with no database at all, for
example to print the batches while developing.

## Example service

`examples/upload-api` is a small HTTP service for importing customers:

- [customer.go](examples/upload-api/customer.go): the table and column rules as one `loader.Config`
- [request.go](examples/upload-api/request.go): multipart parsing, query parameters and error-to-status mapping
- [handler.go](examples/upload-api/handler.go): bearer-token auth, a concurrency limit, and the transaction with
  its commit or abort decision
- [schema.sql](examples/upload-api/schema.sql) and a [sample file](examples/upload-api/sample/customers.csv)

Run it (the API key must be at least 16 characters; `ADDR` defaults to `127.0.0.1:8080`):

```sh
psql "$DATABASE_URL" -f examples/upload-api/schema.sql
UPLOAD_API_KEY=local-dev-key-12345 DATABASE_URL="$DATABASE_URL" go run ./examples/upload-api
```

Upload the sample file:

```sh
curl -X POST "http://127.0.0.1:8080/v1/customers/import?on_error=skip" \
  -H "Authorization: Bearer local-dev-key-12345" \
  -F "file=@examples/upload-api/sample/customers.csv"
```

The response is the loader's report plus `committed`:

```json
{
  "committed": true,
  "completed": true,
  "rows_read": 9,
  "rows_accepted": 5,
  "rows_rejected": 4,
  "rows_written": 5,
  "errors": [{"row": 4, "column": "email", "message": "email is not a valid address"}]
}
```

The counts are illustrative; they depend on the rules in `customer.go`.

| Query parameter | Effect |
| --- | --- |
| `on_error=abort` (default) | Any rejected row rolls the whole file back: `422` with `committed: false` and every bad row listed |
| `on_error=skip` | Commit the valid rows even when some rows are rejected |
| `sheet=NAME` | XLSX sheet to read (`Sheet1` by default) |

Other statuses: `400` for a bad file or header, `401` without the API key, `413` above 10 MiB,
`415` for a file that is not `.csv` or `.xlsx`, `429` when four imports are already running,
`504` on timeout.

## Development

    make check

runs `gofmt`, `go mod tidy`, `go vet` and the tests with the race detector; CI does the same.
The loader tests use an in-memory fake, so they need no database. The integration test in
`examples/upload-api/store_test.go` runs against a real PostgreSQL only when `TEST_DATABASE_URL`
is set, using a throwaway schema:

    TEST_DATABASE_URL='postgres://user:pass@localhost:5432/db?sslmode=disable' make check

The architecture notes are in [docs/architecture.md](docs/architecture.md).

## Status

Early development. The API may still change before a stable release.
