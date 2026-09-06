package download

import (
	"encoding/json"
	"sort"
	"strings"
)

const defaultFilenamePatternPriority = 10

// PreferredFilenamePattern is shared by Torrent and HTTP matching. Lower
// priority numbers win; equal priorities retain their configured row order.
type PreferredFilenamePattern struct {
	Pattern  string `json:"pattern"`
	Priority int    `json:"priority"`
}

func ParsePreferredFilenamePatterns(raw string) []PreferredFilenamePattern {
	raw = strings.TrimSpace(raw)
	patterns := []PreferredFilenamePattern{}
	if raw != "" && json.Unmarshal([]byte(raw), &patterns) != nil {
		for _, value := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == ',' }) {
			patterns = append(patterns, PreferredFilenamePattern{Pattern: strings.TrimSpace(value), Priority: defaultFilenamePatternPriority})
		}
	}
	return normalizePreferredFilenamePatterns(patterns)
}

func EncodePreferredFilenamePatterns(patterns []PreferredFilenamePattern) string {
	encoded, _ := json.Marshal(normalizePreferredFilenamePatterns(patterns))
	return string(encoded)
}

func NormalizePreferredFilenamePatterns(raw string) string {
	return EncodePreferredFilenamePatterns(ParsePreferredFilenamePatterns(raw))
}

func DefaultPreferredFilenamePatterns() string {
	return NormalizePreferredFilenamePatterns("4k688.com@\nhhd800.com@")
}

// ParseBlacklistedFilenamePatterns accepts the settings UI's JSON string list
// and legacy newline/comma text. Matching is case-insensitive; duplicates are
// removed while preserving the first configured spelling and order.
func ParseBlacklistedFilenamePatterns(raw string) []string {
	raw = strings.TrimSpace(raw)
	patterns := []string{}
	if raw != "" && json.Unmarshal([]byte(raw), &patterns) != nil {
		patterns = strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == ',' })
	}
	seen := map[string]bool{}
	clean := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		key := strings.ToLower(pattern)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		clean = append(clean, pattern)
	}
	return clean
}

func NormalizeBlacklistedFilenamePatterns(raw string) string {
	encoded, _ := json.Marshal(ParseBlacklistedFilenamePatterns(raw))
	return string(encoded)
}

func matchesBlacklistedFilename(name string, patterns []string) (bool, string) {
	name = strings.ToLower(name)
	for _, pattern := range patterns {
		if clean := strings.TrimSpace(pattern); clean != "" && strings.Contains(name, strings.ToLower(clean)) {
			return true, clean
		}
	}
	return false, ""
}

func legacyPreferredFilenamePatterns(patterns []string) []PreferredFilenamePattern {
	items := make([]PreferredFilenamePattern, 0, len(patterns))
	for _, pattern := range patterns {
		items = append(items, PreferredFilenamePattern{Pattern: pattern, Priority: defaultFilenamePatternPriority})
	}
	return normalizePreferredFilenamePatterns(items)
}

func defaultPreferredFilenamePatternRows() []PreferredFilenamePattern {
	return ParsePreferredFilenamePatterns(DefaultPreferredFilenamePatterns())
}

func normalizePreferredFilenamePatterns(patterns []PreferredFilenamePattern) []PreferredFilenamePattern {
	seen := map[string]int{}
	clean := make([]PreferredFilenamePattern, 0, len(patterns))
	for _, item := range patterns {
		item.Pattern = strings.TrimSpace(item.Pattern)
		if item.Pattern == "" {
			continue
		}
		if item.Priority < 1 {
			item.Priority = defaultFilenamePatternPriority
		}
		if item.Priority > 999 {
			item.Priority = 999
		}
		key := strings.ToLower(item.Pattern)
		if index, exists := seen[key]; exists {
			if item.Priority < clean[index].Priority {
				clean[index].Priority = item.Priority
			}
			continue
		}
		seen[key] = len(clean)
		clean = append(clean, item)
	}
	sort.SliceStable(clean, func(i, j int) bool { return clean[i].Priority < clean[j].Priority })
	return clean
}
