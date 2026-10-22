package loader_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/mail"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/parmayadav/gobulkin/pkg/loader"
	"github.com/xuri/excelize/v2"
)

// Benchmark data: customers with about 2% invalid emails. See docs/benchmarks.md.
const invalidEvery = 50

func benchCSV(rows int) []byte {
	var b bytes.Buffer
	b.Grow(rows * 60)
	b.WriteString("email,name,age,country,plan\n")
	plans := []string{"free", "pro", ""}
	for i := 0; i < rows; i++ {
		email := fmt.Sprintf("User%d@Example.com", i)
		if i%invalidEvery == 7 {
			email = fmt.Sprintf("bad-email-%d", i)
		}
		fmt.Fprintf(&b, "%s,Name %d,%d,us,%s\n", email, i, 18+i%60, plans[i%3])
	}
	return b.Bytes()
}

func benchXLSX(b *testing.B, rows int) []byte {
	b.Helper()
	file := excelize.NewFile()
	defer file.Close()
	stream, err := file.NewStreamWriter("Sheet1")
	if err != nil {
		b.Fatal(err)
	}
	if err := stream.SetRow("A1", []any{"email", "name", "age", "country", "plan"}); err != nil {
		b.Fatal(err)
	}
	plans := []string{"free", "pro", ""}
	for i := 0; i < rows; i++ {
		email := fmt.Sprintf("User%d@Example.com", i)
		if i%invalidEvery == 7 {
			email = fmt.Sprintf("bad-email-%d", i)
		}
		cell, _ := excelize.CoordinatesToCellName(1, i+2)
		if err := stream.SetRow(cell, []any{email, fmt.Sprintf("Name %d", i), 18 + i%60, "us", plans[i%3]}); err != nil {
			b.Fatal(err)
		}
	}
	if err := stream.Flush(); err != nil {
		b.Fatal(err)
	}
	buf, err := file.WriteToBuffer()
	if err != nil {
		b.Fatal(err)
	}
	return buf.Bytes()
}

func invalidRows(rows int) int {
	count := 0
	for i := 0; i < rows; i++ {
		if i%invalidEvery == 7 {
			count++
		}
	}
	return count
}

func benchLoader(b *testing.B) *loader.Loader {
	b.Helper()
	ld, err := loader.New(loader.Config{
		Table: "bench",
		Columns: []loader.Column{
			{Name: "email", Required: true, Case: loader.Lower, Check: loader.Email},
			{Name: "name", Required: true, MaxLength: 100},
			{Name: "age", Type: loader.Int, Range: &loader.Range{Min: 0, Max: 130}},
			{Name: "country", Case: loader.Upper, Pattern: regexp.MustCompile(`^[A-Z]{2}$`)},
			{Name: "plan", Case: loader.Lower, OneOf: []string{"free", "pro", "enterprise"}, Default: "free"},
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	return ld
}

// discardTx consumes rows the way pgx would, without a database.
type discardTx struct{ rows atomic.Int64 }

func (d *discardTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, src pgx.CopyFromSource) (int64, error) {
	var n int64
	for src.Next() {
		if _, err := src.Values(); err != nil {
			return n, err
		}
		n++
	}
	d.rows.Add(n)
	return n, src.Err()
}

func (d *discardTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func reportRate(b *testing.B, rows int) {
	b.ReportMetric(float64(rows)*float64(b.N)/b.Elapsed().Seconds(), "rows/s")
}

// BenchmarkImportCSV measures parsing, validation and batching, with the database taken out.
func BenchmarkImportCSV(b *testing.B) {
	for _, rows := range []int{10_000, 100_000} {
		b.Run(fmt.Sprintf("rows=%d", rows), func(b *testing.B) {
			data := benchCSV(rows)
			ld := benchLoader(b)
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tx := &discardTx{}
				report, err := ld.ImportFile(context.Background(), tx, "data.csv", bytes.NewReader(data), loader.Options{})
				if err != nil || report.RowsRead != int64(rows) {
					b.Fatalf("report = %+v, err = %v", report, err)
				}
			}
			reportRate(b, rows)
		})
	}
}

func BenchmarkImportXLSX(b *testing.B) {
	const rows = 20_000
	data := benchXLSX(b, rows)
	ld := benchLoader(b)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		report, err := ld.ImportFile(context.Background(), &discardTx{}, "data.xlsx", bytes.NewReader(data), loader.Options{})
		if err != nil || report.RowsRead != rows {
			b.Fatalf("report = %+v, err = %v", report, err)
		}
	}
	reportRate(b, rows)
}

// csvStream generates the benchmark CSV lazily, so the input never sits in memory.
type csvStream struct {
	rows, next int
	header     bool
	pending    []byte
}

func newCSVStream(rows int) *csvStream { return &csvStream{rows: rows} }

func (s *csvStream) Read(p []byte) (int, error) {
	for len(s.pending) == 0 {
		switch {
		case !s.header:
			s.header = true
			s.pending = []byte("email,name,age,country,plan\n")
		case s.next >= s.rows:
			return 0, io.EOF
		default:
			i := s.next
			s.next++
			email := fmt.Sprintf("User%d@Example.com", i)
			if i%invalidEvery == 7 {
				email = fmt.Sprintf("bad-email-%d", i)
			}
			s.pending = fmt.Appendf(s.pending[:0], "%s,Name %d,%d,us,%s\n", email, i, 18+i%60, []string{"free", "pro", ""}[i%3])
		}
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

// BenchmarkMemoryCSV reports the peak heap growth while importing a streamed CSV of increasing
// size. A flat figure means memory depends on the batch size, not on the file.
func BenchmarkMemoryCSV(b *testing.B) {
	for _, rows := range []int{10_000, 100_000, 1_000_000} {
		b.Run(fmt.Sprintf("rows=%d", rows), func(b *testing.B) {
			size, _ := io.Copy(io.Discard, newCSVStream(rows))
			ld := benchLoader(b)
			var peak uint64
			for i := 0; i < b.N; i++ {
				peak = max(peak, peakHeapGrowth(func() {
					report, err := ld.ImportFile(context.Background(), &discardTx{}, "data.csv", newCSVStream(rows), loader.Options{})
					if err != nil || report.RowsRead != int64(rows) {
						b.Fatalf("report = %+v, err = %v", report, err)
					}
				}))
			}
			b.ReportMetric(float64(peak)/1e6, "peak-heap-MB")
			b.ReportMetric(float64(size)/1e6, "file-MB")
		})
	}
}

// peakHeapGrowth runs fn and returns the highest sampled HeapAlloc above the starting level.
func peakHeapGrowth(fn func()) uint64 {
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	var peak atomic.Uint64
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		var stats runtime.MemStats
		for {
			select {
			case <-stop:
				return
			default:
				runtime.ReadMemStats(&stats)
				if stats.HeapAlloc > peak.Load() {
					peak.Store(stats.HeapAlloc)
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()

	fn()
	close(stop)
	<-stopped
	if peak.Load() < base.HeapAlloc {
		return 0
	}
	return peak.Load() - base.HeapAlloc
}

// The comparison below needs a database: set TEST_DATABASE_URL.

const benchInsert = "INSERT INTO bench (email, name, age, country, plan) VALUES ($1, $2, $3, $4, $5)"

var (
	benchCountry = regexp.MustCompile(`^[A-Z]{2}$`)
	benchPlans   = map[string]bool{"free": true, "pro": true, "enterprise": true}
)

// naiveRow is the straightforward hand-written equivalent of the loader's column rules.
func naiveRow(rec []string) ([]any, bool) {
	email := strings.ToLower(strings.TrimSpace(rec[0]))
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || len(email) > 254 {
		return nil, false
	}
	name := strings.TrimSpace(rec[1])
	if name == "" || len([]rune(name)) > 100 {
		return nil, false
	}
	var age any
	if raw := strings.TrimSpace(rec[2]); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 || n > 130 {
			return nil, false
		}
		age = n
	}
	var country any
	if raw := strings.ToUpper(strings.TrimSpace(rec[3])); raw != "" {
		if !benchCountry.MatchString(raw) {
			return nil, false
		}
		country = raw
	}
	plan := strings.ToLower(strings.TrimSpace(rec[4]))
	if plan == "" {
		plan = "free"
	}
	if !benchPlans[plan] {
		return nil, false
	}
	return []any{email, name, age, country, plan}, true
}

// naiveImport reads with encoding/csv, validates, and inserts one row per statement.
func naiveImport(ctx context.Context, tx pgx.Tx, data []byte, batchSize int) (accepted int, err error) {
	reader := csv.NewReader(bytes.NewReader(data))
	if _, err := reader.Read(); err != nil {
		return 0, err
	}
	batch := &pgx.Batch{}
	flush := func() error {
		if batch.Len() == 0 {
			return nil
		}
		results := tx.SendBatch(ctx, batch)
		for i := 0; i < batch.Len(); i++ {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return err
			}
		}
		batch = &pgx.Batch{}
		return results.Close()
	}
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return accepted, err
		}
		row, ok := naiveRow(rec)
		if !ok {
			continue
		}
		accepted++
		if batchSize == 0 {
			if _, err := tx.Exec(ctx, benchInsert, row...); err != nil {
				return accepted, err
			}
			continue
		}
		batch.Queue(benchInsert, row...)
		if batch.Len() >= batchSize {
			if err := flush(); err != nil {
				return accepted, err
			}
		}
	}
	return accepted, flush()
}

func BenchmarkPostgres(b *testing.B) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		b.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		b.Fatal(err)
	}
	defer admin.Close(ctx)
	schema := fmt.Sprintf("gobulkin_bench_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA %[1]s;
		CREATE TABLE %[1]s.bench (email text, name text, age integer, country text, plan text)`, schema)); err != nil {
		b.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") }()

	cfg, _ := pgx.ParseConfig(dsn)
	cfg.RuntimeParams["search_path"] = schema
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close(ctx)

	count := func() int {
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM bench").Scan(&n); err != nil {
			b.Fatal(err)
		}
		return n
	}
	ld := benchLoader(b)

	variants := []struct {
		name string
		run  func(tx pgx.Tx, data []byte) (int, error)
	}{
		{"loader", func(tx pgx.Tx, data []byte) (int, error) {
			report, err := ld.ImportFile(ctx, tx, "data.csv", bytes.NewReader(data), loader.Options{})
			if err != nil {
				return 0, err
			}
			return int(report.RowsWritten), nil
		}},
		{"naive-insert-per-row", func(tx pgx.Tx, data []byte) (int, error) { return naiveImport(ctx, tx, data, 0) }},
		{"naive-batched-insert", func(tx pgx.Tx, data []byte) (int, error) { return naiveImport(ctx, tx, data, 1000) }},
	}

	for _, rows := range []int{10_000, 100_000} {
		data := benchCSV(rows)
		want := rows - invalidRows(rows)
		for _, variant := range variants {
			// One statement per row takes minutes at 100k rows; its rate is flat, so 10k is enough.
			if rows > 10_000 && variant.name == "naive-insert-per-row" {
				continue
			}
			b.Run(fmt.Sprintf("rows=%d/%s", rows, variant.name), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					if _, err := conn.Exec(ctx, "TRUNCATE bench"); err != nil {
						b.Fatal(err)
					}
					tx, err := conn.Begin(ctx)
					if err != nil {
						b.Fatal(err)
					}
					b.StartTimer()

					got, err := variant.run(tx, data)
					if err == nil {
						err = tx.Commit(ctx)
					} else {
						_ = tx.Rollback(ctx)
					}
					if err != nil || got != want {
						b.Fatalf("accepted = %d, want %d, err = %v", got, want, err)
					}
					if stored := count(); stored != want {
						b.Fatalf("stored = %d, want %d", stored, want)
					}
				}
				reportRate(b, rows)
			})
		}
	}
}
