package cmd

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "success", err: nil, want: 0},
		{name: "plain error", err: errors.New("boom"), want: 1},
		{name: "flow interrupted by signal", err: errFlowInterrupted, want: 143},
		{name: "wrapped exit code", err: fmt.Errorf("serve: %w", errFlowInterrupted), want: 143},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := exitStatus(tt.err); got != tt.want {
				t.Errorf("exitStatus(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}
