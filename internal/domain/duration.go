package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// scheduleDurationTokenPattern matches one <number><unit> component of a
// duration string using the two extra units ParseScheduleDuration accepts
// beyond what time.ParseDuration itself understands: d (day) and w (week).
var scheduleDurationTokenPattern = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)(d|w)`)

// ParseScheduleDuration parses a schedule interval string the same way
// time.ParseDuration does, additionally accepting "d" (24h) and "w" (7d =
// 168h) as unit suffixes - so a schedule interval setting can be written
// the way people naturally type it ("7d", "2w", "1w3d"), not just in
// time.ParseDuration's own ns/us/ms/s/m/h units.
//
// Every scheduled-job interval setting (the Quick/Full/New Release Only
// scrape schedules, Download monitoring's recent/older search schedules,
// and StashApp's local-library/Watchlist-tag sync schedules) parses through
// this instead of calling time.ParseDuration directly, so they all accept
// the same set of units consistently. Before this existed, typing "7d" into
// one of these fields looked accepted (the raw text was saved and echoed
// back), but every scheduler loop's own time.ParseDuration call silently
// rejected it and fell back to that schedule's built-in default interval -
// so the schedule quietly ran on a completely different cadence than the
// one actually configured, with no error surfaced anywhere.
func ParseScheduleDuration(s string) (time.Duration, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("empty duration")
	}
	converted := scheduleDurationTokenPattern.ReplaceAllStringFunc(trimmed, func(tok string) string {
		m := scheduleDurationTokenPattern.FindStringSubmatch(tok)
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return tok
		}
		hours := n * 24
		if strings.EqualFold(m[2], "w") {
			hours *= 7
		}
		return strconv.FormatFloat(hours, 'f', -1, 64) + "h"
	})
	return time.ParseDuration(converted)
}
