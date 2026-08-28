package version

import (
	"strings"
	"testing"
)

func TestCurrentUsesEmbeddedReleaseVersion(t *testing.T) {
	previous := Value
	Value = ""
	t.Cleanup(func() { Value = previous })
	want := "v" + strings.TrimSpace(source)
	if got := Current(); got != want {
		t.Fatalf("Current() = %q, want %s", got, want)
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
