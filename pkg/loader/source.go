package loader

import (
	"io"
	"path/filepath"
	"strings"

	"github.com/parmayadav/gobulkin/pkg/core"
)

const DefaultSheet = "Sheet1"

// openSource picks the reader from the file extension. XLSX is buffered in memory.
func openSource(filename string, content io.Reader, sheet string) (core.Source, error) {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".csv":
		src, err := newCSVReader(content)
		if err != nil {
			return nil, err
		}
		return src, nil
	case ".xlsx":
		if sheet == "" {
			sheet = DefaultSheet
		}
		src, err := newXLSXReader(content, sheet)
		if err != nil {
			return nil, err
		}
		return src, nil
	default:
		return nil, ErrUnsupportedFormat
	}
}
