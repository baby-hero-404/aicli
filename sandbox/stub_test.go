package sandbox

import (
	"context"
	"strings"
	"testing"
)

func TestStubRuntime_WritesStdoutToOutput(t *testing.T) {
	var out strings.Builder
	res, err := NewStubRuntime().Run(context.Background(), CommandRequest{
		Command: []string{"echo", "hi"},
		Output:  &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.String() != res.Stdout || !strings.Contains(out.String(), "echo hi") {
		t.Errorf("Output = %q, Stdout = %q", out.String(), res.Stdout)
	}
}
