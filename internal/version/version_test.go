package version

import "testing"

func TestCurrentUsesEmbeddedReleaseVersion(t *testing.T) {
	previous := Value
	Value = ""
	t.Cleanup(func() { Value = previous })
	if got := Current(); got != "v1.0.28" {
		t.Fatalf("Current() = %q, want v1.0.28", got)
	}
}

func TestCurrentNormalizesLinkerOverride(t *testing.T) {
	previous := Value
	Value = "2.3.4"
	t.Cleanup(func() { Value = previous })
	if got := Current(); got != "v2.3.4" {
		t.Fatalf("Current() = %q, want v2.3.4", got)
	}
}
