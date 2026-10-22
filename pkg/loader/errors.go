package loader

import (
	"errors"
	"io"
)

var ErrUnsupportedFormat = errors.New("loader: only .csv and .xlsx files are supported")

// InputError is a problem with the file (empty, unreadable, malformed, bad header) rather
// than the server. Message is safe to show to the uploader.
type InputError struct {
	Message string
	Err     error
}

func (e *InputError) Error() string { return e.Message }
func (e *InputError) Unwrap() error { return e.Err }

// openError classifies a failure to open the file; Err keeps the cause.
func openError(err error) error {
	if errors.Is(err, ErrUnsupportedFormat) {
		return err
	}
	message := "could not read the file"
	if errors.Is(err, io.EOF) {
		message = "file is empty or has no header row"
	}
	return &InputError{Message: message, Err: err}
}
