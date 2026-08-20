package cli

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "nil", want: ExitOK},
		{name: "broken pipe", err: fmt.Errorf("write: %w", syscall.EPIPE), want: ExitOK},
		{name: "coded", err: newExitError(ExitConflict, "conflict"), want: ExitConflict},
		{name: "wrapped coded", err: fmt.Errorf("outer: %w", newExitError(ExitContext, "context")), want: ExitContext},
		{name: "operation", err: errors.New("failure"), want: ExitOperation},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := exitCode(test.err); got != test.want {
				t.Fatalf("exitCode(%v) = %d, want %d", test.err, got, test.want)
			}
		})
	}
}
