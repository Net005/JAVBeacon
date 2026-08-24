package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var source string

// Value may be overridden at build time with -ldflags -X. The checked-in
// VERSION file remains the shared fallback for local and container builds.
var Value string

func Current() string {
	value := strings.TrimSpace(Value)
	if value == "" {
		value = strings.TrimSpace(source)
	}
	if value == "" {
		return "dev"
	}
	if strings.HasPrefix(value, "v") {
		return value
	}
	return "v" + value
}
