# Architecture

```
gobulkin/
├── pkg/
│   ├── core/        Import, hooks, Record, Header, Source (no third-party deps)
│   ├── postgres/    COPY Writer and Upserter (pgx)
│   └── loader/      Column spec, CSV and XLSX readers, row rules, report
├── examples/
│   └── upload-api/  HTTP service built on pkg/loader
└── docs/
```

`core` imports nothing else from the module, so it adds no file-format or database
dependencies. `postgres` and `loader` build on it, and `loader` also uses `postgres`.

## Flow of one import

1. The caller parses the upload, begins a transaction and calls `loader.ImportFile`.
2. `loader` picks the CSV or XLSX reader from the file extension and checks the header.
3. `core.Import` pulls rows from that reader and runs the loader's hooks: trim and case,
   then the column rules, then the duplicate-key check.
4. Rejected rows are recorded in the report and skipped; accepted rows are converted and
   buffered into batches.
5. Each batch goes through `postgres` (COPY into the table, or into a staging table when a
   `Key` is set). After the last batch a keyed import merges the staging table.
6. `ImportFile` returns the report. The caller commits or rolls back.

The loader never touches the transaction's lifecycle, so the all-or-nothing or partial-commit
decision stays with the caller.

## Loader files

| File | Role |
| --- | --- |
| `config.go` | `Config`, `Column`, `Options` and their validation |
| `loader.go` | `Loader`, `ImportFile`, the `Tx` interface, writing and merging |
| `rules.go` | Header checks and per-row normalize, validate and convert |
| `report.go` | `Report` and the capped issue log |
| `errors.go` | `ErrUnsupportedFormat` and `InputError` |
| `source.go`, `csvreader.go`, `xlsxreader.go` | Reader selection and the two readers |

See the [README](../README.md#how-it-works) for the diagrams, including the per-row flow of
`core.Import`.
