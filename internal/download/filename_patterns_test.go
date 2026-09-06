package download

import "testing"

func TestParsePreferredFilenamePatternsMigratesLegacyValuesAtPriorityTen(t *testing.T) {
	patterns := ParsePreferredFilenamePatterns("4k688.com@\nhhd800.com@")
	if len(patterns) != 2 {
		t.Fatalf("patterns = %+v, want two rows", patterns)
	}
	for _, pattern := range patterns {
		if pattern.Priority != 10 {
			t.Fatalf("legacy pattern %+v priority = %d, want 10", pattern, pattern.Priority)
		}
	}
	if got := NormalizePreferredFilenamePatterns("4k688.com@\nhhd800.com@"); got != `[{"pattern":"4k688.com@","priority":10},{"pattern":"hhd800.com@","priority":10}]` {
		t.Fatalf("normalized legacy patterns = %q", got)
	}
}

func TestParsePreferredFilenamePatternsOrdersLowestPriorityFirst(t *testing.T) {
	patterns := ParsePreferredFilenamePatterns(`[
		{"pattern":"priority-ten@","priority":10},
		{"pattern":"priority-one@","priority":1},
		{"pattern":"priority-five@","priority":5}
	]`)
	if len(patterns) != 3 || patterns[0].Pattern != "priority-one@" || patterns[1].Pattern != "priority-five@" || patterns[2].Pattern != "priority-ten@" {
		t.Fatalf("priority order = %+v", patterns)
	}
}

func TestParsePreferredFilenamePatternsKeepsBestDuplicatePriority(t *testing.T) {
	patterns := ParsePreferredFilenamePatterns(`[
		{"pattern":"same@","priority":10},
		{"pattern":"SAME@","priority":1}
	]`)
	if len(patterns) != 1 || patterns[0].Priority != 1 {
		t.Fatalf("deduplicated patterns = %+v", patterns)
	}
}

func TestParseBlacklistedFilenamePatternsAcceptsJSONAndLegacyValues(t *testing.T) {
	for _, raw := range []string{`["sample", "CAMRIP", "sample"]`, "sample\nCAMRIP\nSAMPLE"} {
		patterns := ParseBlacklistedFilenamePatterns(raw)
		if len(patterns) != 2 || patterns[0] != "sample" || patterns[1] != "CAMRIP" {
			t.Fatalf("parsed blacklist %q = %+v", raw, patterns)
		}
	}
	if got := NormalizeBlacklistedFilenamePatterns("sample\nCAMRIP"); got != `["sample","CAMRIP"]` {
		t.Fatalf("normalized blacklist = %q", got)
	}
}

func TestBlacklistedFilenameMatchingIsPartialAndCaseInsensitive(t *testing.T) {
	matched, pattern := matchesBlacklistedFilename("Trusted@PRED-888-CamRip.MP4", []string{"camrip"})
	if !matched || pattern != "camrip" {
		t.Fatalf("matched=%v pattern=%q", matched, pattern)
	}
}
