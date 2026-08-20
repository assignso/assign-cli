package cli

import (
	"errors"
	"fmt"
	"syscall"
)

const (
	ExitOK             = 0
	ExitOperation      = 1
	ExitArguments      = 2
	ExitAuthentication = 3
	ExitContext        = 4
	ExitConflict       = 5
	ExitCancellation   = 6
	ExitInterrupt      = 130
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func newExitError(code int, format string, args ...any) error {
	return &exitError{code: code, err: fmt.Errorf(format, args...)}
}

func exitCode(err error) int {
	if err == nil || isBrokenPipe(err) {
		return ExitOK
	}

	var coded *exitError
	if errors.As(err, &coded) {
		return coded.code
	}

	return ExitOperation
}

func isBrokenPipe(err error) bool {
	return errors.Is(err, syscall.EPIPE)
}
