package loader

import (
	"context"
	"io"

	"github.com/parmayadav/gobulkin/pkg/core"
	"github.com/xuri/excelize/v2"
)

// Caps decompressed workbook size.
const maxUncompressedBytes int64 = 256 << 20

type xlsxReader struct {
	file    *excelize.File
	rows    *excelize.Rows
	headers []string
	row     int
}

// newXLSXReader buffers the whole workbook in memory.
func newXLSXReader(r io.Reader, sheet string) (*xlsxReader, error) {
	file, err := excelize.OpenReader(r, excelize.Options{UnzipSizeLimit: maxUncompressedBytes})
	if err != nil {
		return nil, err
	}

	rows, err := file.Rows(sheet)
	if err != nil {
		_ = file.Close()
		return nil, err
	}

	reader := &xlsxReader{file: file, rows: rows}
	if !rows.Next() {
		_ = reader.Close()
		return nil, io.EOF
	}
	if reader.headers, err = rows.Columns(); err != nil {
		_ = reader.Close()
		return nil, err
	}
	return reader, nil
}

func (x *xlsxReader) Headers() []string {
	return append([]string(nil), x.headers...)
}

func (x *xlsxReader) Next(ctx context.Context) (core.Record, error) {
	if err := ctx.Err(); err != nil {
		return core.Record{}, err
	}

	if !x.rows.Next() {
		if err := x.rows.Error(); err != nil {
			return core.Record{}, err
		}
		return core.Record{}, io.EOF
	}

	values, err := x.rows.Columns()
	if err != nil {
		return core.Record{}, err
	}

	x.row++
	return core.Record{Number: x.row, Values: values}, nil
}

func (x *xlsxReader) Close() error {
	rowsErr := x.rows.Close()
	fileErr := x.file.Close()

	if rowsErr != nil {
		return rowsErr
	}
	return fileErr
}
