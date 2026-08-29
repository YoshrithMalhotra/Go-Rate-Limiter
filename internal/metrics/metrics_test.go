package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestRegistryRenderIncludesCountersAndHistogram(t *testing.T) {
	registry := NewRegistry()
	registry.ObserveCheck("login", "gcra", "allowed", 2*time.Millisecond)

	output := registry.Render()

	expected := []string{
		`gobouncer_checks_total{policy="login",algorithm="gcra",outcome="allowed"} 1`,
		`gobouncer_check_duration_seconds_bucket{policy="login",algorithm="gcra",outcome="allowed",le="0.005"} 1`,
		`gobouncer_check_duration_seconds_count{policy="login",algorithm="gcra",outcome="allowed"} 1`,
	}
	for _, line := range expected {
		if !strings.Contains(output, line) {
			t.Fatalf("expected metrics output to contain %q, got:\n%s", line, output)
		}
	}
}
