# Benchmarks

Numbers from one machine, one run of the suite. They show relative cost and how memory scales,
not what your production database will do.

## Setup

| | |
| --- | --- |
| Machine | Apple M4 Pro (14 cores), 48 GiB RAM, macOS |
| Go | 1.27.1 darwin/arm64 (the module requires 1.25) |
| PostgreSQL | 16.15 in a `postgres:16-alpine` Docker container, default configuration, reached over loopback |
| Data | Customer rows: `email,name,age,country,plan`, about 44 bytes each. Every 50th row has an invalid email (2 percent rejected) |
| Rules | Required email (lower-cased, `loader.Email`), required name up to 100 characters, age 0 to 130, two-letter country, plan one of three values with a default |
| Method | `go test -bench ... -benchtime=5x -count=3`; tables show the median of the three runs |

Reproduce:

```sh
# database-free pipeline, XLSX and memory benchmarks
go test -run '^$' -bench 'ImportCSV|ImportXLSX|MemoryCSV' -benchtime=5x -count=3 -benchmem ./pkg/loader/

# the PostgreSQL comparison (needs a database)
TEST_DATABASE_URL='postgres://postgres:pw@localhost:5432/db?sslmode=disable' \
  go test -run '^$' -bench Postgres -benchtime=5x -count=3 -benchmem ./pkg/loader/
```

The benchmarks live in [pkg/loader/bench_test.go](../pkg/loader/bench_test.go).

## Throughput without a database

The loader reads, trims, validates and converts every row and batches the accepted ones into a
transaction that discards them. This is the cost of the library itself.

| Input | Rows | Median rows/s | Allocated per import |
| --- | --- | --- | --- |
| CSV | 10,000 | 1,064,000 | 5.7 MB |
| CSV | 100,000 | 1,099,000 | 56 MB (about 560 bytes and 18 allocations per row) |
| XLSX | 20,000 | 149,000 | 140 MB |

XLSX is about seven times slower than CSV because the workbook is unzipped and parsed as XML.
The allocated figure is garbage produced over the run, not memory held at once.

## Memory

A streamed CSV (generated on the fly, so the input is never in memory) imported into a
discarding transaction, sampling the heap every millisecond:

| Rows | File size | Peak heap growth |
| --- | --- | --- |
| 10,000 | 0.4 MB | 2.7 MB |
| 100,000 | 4.2 MB | 2.7 MB |
| 1,000,000 | 44 MB | 2.7 MB |

Memory follows `BatchSize` (1000 by default), not the size of the file. This holds for CSV. An
XLSX upload is buffered in memory by design, so cap the upload size; see the README.

## Against a straightforward implementation

The same file, the same 2 percent of invalid rows, written to a real table inside one
transaction. The two baselines are what most people would write by hand:

- **naive-insert-per-row**: `encoding/csv`, hand-written equivalents of the rules above, one
  `INSERT` per accepted row.
- **naive-batched-insert**: the same, but queued into a `pgx.Batch` of 1000 statements.

Every variant is checked to accept the same rows and to store exactly that many, so the
comparison covers equivalent behaviour.

| Rows | Variant | Median rows/s | Time per import |
| --- | --- | --- | --- |
| 10,000 | **loader** (validate + COPY) | 280,000 | 36 ms |
| 10,000 | naive-batched-insert | 256,000 | 39 ms |
| 10,000 | naive-insert-per-row | 4,100 | 2.4 s |
| 100,000 | **loader** (validate + COPY) | 308,000 | 325 ms |
| 100,000 | naive-batched-insert | 252,000 | 396 ms |

What to take from it:

- Row-at-a-time inserts are the trap: about 70 times slower than COPY, and the gap grows with
  the file. At 100,000 rows an import took about 24 seconds in an interrupted earlier run, at the
  same 4,100 rows/s, so it is not included in the table above.
- A well-written batched insert is close at small sizes. COPY pulls ahead as files grow (about
  20 percent at 100,000 rows here) and allocates about 40 percent less (85 MB against 138 MB at
  100,000 rows).
- The loader's advantage over a hand-written batch is mostly not speed. It is that you declare
  the rules instead of writing and testing them, and that every bad row is reported in one pass.

## Caveats

- One machine, loopback networking, no concurrent imports. A real network adds latency to each
  round trip, which hurts the per-row variant most and COPY least.
- The naive implementations are mine, written to match the loader's rules. A hand-tuned
  implementation could narrow the gap with the batched variant.
- Only CSV memory is measured. XLSX is held in memory and parsed by excelize; its allocation
  figure is above.
- Run `go test -bench` on your own hardware and data before drawing conclusions for production.
