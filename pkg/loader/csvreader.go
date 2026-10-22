package loader

import (
	"context"
	"encoding/csv"
	"io"

	"github.com/parmayadav/gobulkin/pkg/core"
)

type csvReader struct {
	reader  *csv.Reader
	headers []string
	row     int
}

func newCSVReader(r io.Reader) (*csvReader, error) {
	reader := csv.NewReader(r)
	headers, err := reader.Read()
	if err != nil {
		return nil, err
	}
	return &csvReader{reader: reader, headers: headers}, nil
}

func (c *csvReader) Headers() []string {
	return append([]string(nil), c.headers...)
}

func (c *csvReader) Next(ctx context.Context) (core.Record, error) {
	if err := ctx.Err(); err != nil {
		return core.Record{}, err
	}

	values, err := c.reader.Read()
	if err != nil {
		return core.Record{}, err
	}

	c.row++
	return core.Record{Number: c.row, Values: values}, nil
}

func (c *csvReader) Close() error {
	return nil
}
