package main

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/parmayadav/gobulkin/pkg/loader"
)

const (
	uploadField        = "file"
	maxUploadBytes     = 10 << 20
	statusClientClosed = 499
)

var errNoFile = errors.New("expected a multipart/form-data upload with a file")

// importParams are the query parameters of an upload.
type importParams struct {
	options loader.Options
	// commitPartial keeps the valid rows when some rows were rejected.
	commitPartial bool
}

// parseImportParams reads ?sheet= and ?on_error=abort|skip (abort is the default).
func parseImportParams(r *http.Request) (importParams, error) {
	query := r.URL.Query()
	params := importParams{options: loader.Options{Sheet: query.Get("sheet")}}

	switch query.Get("on_error") {
	case "", "abort":
	case "skip":
		params.commitPartial = true
	default:
		return importParams{}, errors.New(`on_error must be "abort" or "skip"`)
	}
	return params, nil
}

// firstFile streams to the first file part without buffering the whole form.
func firstFile(r *http.Request) (*multipart.Part, error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, errNoFile
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, errNoFile
		}
		if err != nil {
			return nil, err
		}
		if part.FormName() == uploadField && part.FileName() != "" {
			return part, nil
		}
		_ = part.Close()
	}
}

// statusFor maps an import error to a status and a client-safe message.
func statusFor(err error) (int, string) {
	var tooLarge *http.MaxBytesError
	var inputErr *loader.InputError

	switch {
	case errors.As(err, &tooLarge):
		return http.StatusRequestEntityTooLarge, "file is too large"
	case errors.Is(err, errNoFile):
		return http.StatusBadRequest, errNoFile.Error()
	case errors.Is(err, loader.ErrUnsupportedFormat):
		return http.StatusUnsupportedMediaType, "only .csv and .xlsx files are supported"
	case errors.As(err, &inputErr):
		return http.StatusBadRequest, inputErr.Message
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "import timed out"
	case errors.Is(err, context.Canceled):
		return statusClientClosed, "request cancelled"
	default:
		return http.StatusInternalServerError, "import failed"
	}
}
