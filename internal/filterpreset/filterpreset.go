// Package filterpreset holds the shared, delicate logic for turning a saved
// Release Library filter set (domain.FilterPreset.State) into the exact
// domain.ReleaseFilter the Release Library itself would use for it, resolving
// that filter down to an ordered list of release IDs, and sanitizing a
// preset's free-text name before it becomes a single-string genre/tag value.
//
// Both internal/jellyfin (real Jellyfin BoxSet collections) and
// internal/silo (a "Collection: <name>" genre/tag substitute, since Silo's
// plugin SDK has no collection-management capability) need to answer the
// exact same question - "which releases currently match saved filter set
// X, in what order" - the exact same way, or a release could show up in one
// integration's collection/tag but not the other's for the same saved
// filter. FromState in particular must keep mirroring app.js's
// releaseQuery()/releaseFilterFromQuery pairing exactly (see its own doc
// comment) - a property that is far too easy to lose track of if this logic
// is copy-pasted into two service packages that evolve independently.
package filterpreset

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

// State mirrors the shape the web UI saves as a FilterPreset's State (see
// app.js currentReleaseFilterState) - only the fields relevant to
// selecting/ordering releases are decoded; unknown fields are ignored.
type State struct {
	ActiveTab        string          `json:"activeTab"`
	Category         string          `json:"category"`
	Entries          json.RawMessage `json:"entries"`
	Search           string          `json:"search"`
	WildcardLogic    string          `json:"wildcardLogic"`
	SearchExpression json.RawMessage `json:"searchExpression"`
	SortField        string          `json:"sortField"`
	SortDirection    string          `json:"sortDirection"`
	HideLocal        bool            `json:"hideLocal"`
	HideMonitored    bool            `json:"hideMonitored"`
	ShowNonPreferred bool            `json:"showNonPreferred"`
	Watchlist        bool            `json:"watchlist"`
	ReleasedMinDays  string          `json:"releasedMinDays"`
	ReleasedMaxDays  string          `json:"releasedMaxDays"`
	UpcomingMaxDays  string          `json:"upcomingMaxDays"`
}

// FromState reproduces app.js's releaseQuery()/releaseFilterFromQuery
// pairing in Go, so a saved filter set resolves to precisely the same
// domain.ReleaseFilter the Release Library itself would send for it - same
// search, category/entries, structured conditions, wildcard logic, sort, and
// (for the Released/Upcoming tabs) the same "days ago/days from now" window,
// anchored on today since the UI never persists an explicit start date.
func FromState(raw json.RawMessage, settings map[string]string) (domain.ReleaseFilter, bool) {
	var state State
	if len(raw) == 0 || json.Unmarshal(raw, &state) != nil {
		return domain.ReleaseFilter{}, false
	}
	f := domain.ReleaseFilter{
		Search:           state.Search,
		SearchWildcards:  true,
		Status:           state.ActiveTab,
		Category:         state.Category,
		WildcardLogic:    state.WildcardLogic,
		Watchlist:        state.Watchlist,
		HideLocal:        state.HideLocal,
		HideMonitored:    state.HideMonitored,
		ShowNonPreferred: state.ShowNonPreferred,
		Sort:             state.SortField,
		Direction:        state.SortDirection,
	}
	if len(state.Entries) > 0 && string(state.Entries) != "null" {
		f.Entries = string(state.Entries)
	}
	if len(state.SearchExpression) > 0 && string(state.SearchExpression) != "null" {
		f.SearchExpression = string(state.SearchExpression)
	}
	if !f.ShowNonPreferred {
		f.IgnoreTags = domain.ParseIgnoreList(settings["ignore_tags"])
		f.IgnoreTitles = domain.ParseIgnoreList(settings["ignore_titles"])
		f.UsePreferred = len(f.IgnoreTags) > 0 || len(f.IgnoreTitles) > 0
	}
	today := time.Now().UTC()
	switch state.ActiveTab {
	case "released":
		if minDays, err := strconv.Atoi(strings.TrimSpace(state.ReleasedMinDays)); err == nil {
			f.MaxReleaseDate = today.AddDate(0, 0, -minDays).Format("2006-01-02")
		}
		if maxDays, err := strconv.Atoi(strings.TrimSpace(state.ReleasedMaxDays)); err == nil {
			f.MinReleaseDate = today.AddDate(0, 0, -maxDays).Format("2006-01-02")
		}
	case "upcoming":
		if maxDays, err := strconv.Atoi(strings.TrimSpace(state.UpcomingMaxDays)); err == nil {
			f.MaxReleaseDate = today.AddDate(0, 0, maxDays).Format("2006-01-02")
		}
	}
	return f, true
}

// ResolveReleaseIDs finds the local Stash-linked members of a saved filter
// for media-server collections. The UI's Hide local and Hide monitored flags
// are presentation filters for discovering new releases; applying either here
// would hide eligible collection members. All other criteria and sort order
// remain the same as the saved filter.
func ResolveReleaseIDs(ctx context.Context, st store.Store, filter domain.ReleaseFilter) ([]int64, error) {
	ids := []int64{}
	// Only local Stash-linked releases can appear in either media server.
	// Apply this in SQL before paging instead of scanning every remote title.
	filter.StashLinked = true
	filter.HideLocal = false
	filter.HideMonitored = false
	filter.SearchExpression = collectionSearchExpression(filter.SearchExpression)
	filter.Limit = 500
	for offset := 0; ; offset += 500 {
		filter.Offset = offset
		rows, err := st.Releases(ctx, filter)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.Local && r.StashSceneID != "" {
				ids = append(ids, r.ID)
			}
		}
		if len(rows) < 500 {
			break
		}
	}
	return ids, nil
}

// collectionSearchExpression removes UI availability predicates from a saved
// structured search. A collection contains local items by definition, so a
// saved "local=false" or "monitored=false" condition would otherwise make
// its Silo/Jellyfin membership empty even when the library view has matches.
func collectionSearchExpression(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return raw
	}
	var root map[string]any
	if json.Unmarshal([]byte(raw), &root) != nil {
		return raw
	}
	if !pruneAvailabilityConditions(root) {
		return ""
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return raw
	}
	return string(encoded)
}

func pruneAvailabilityConditions(group map[string]any) bool {
	if rawConditions, ok := group["conditions"].([]any); ok {
		kept := make([]any, 0, len(rawConditions))
		for _, raw := range rawConditions {
			condition, ok := raw.(map[string]any)
			if !ok {
				kept = append(kept, raw)
				continue
			}
			field, _ := condition["field"].(string)
			if strings.EqualFold(field, "local") || strings.EqualFold(field, "monitored") {
				continue
			}
			kept = append(kept, condition)
		}
		group["conditions"] = kept
	}
	if rawGroups, ok := group["groups"].([]any); ok {
		kept := make([]any, 0, len(rawGroups))
		for _, raw := range rawGroups {
			subgroup, ok := raw.(map[string]any)
			if !ok || pruneAvailabilityConditions(subgroup) {
				kept = append(kept, raw)
			}
		}
		group["groups"] = kept
	}
	conditions, _ := group["conditions"].([]any)
	groups, _ := group["groups"].([]any)
	return len(conditions) > 0 || len(groups) > 0
}

// MaxTagNameRunes bounds a saved filter set's name once it becomes a
// "Collection: <name>" genre/tag value - generous for any real preset name,
// but enough to stop a pathological one from producing an oversized tag.
const MaxTagNameRunes = 80

// SanitizeTagName cleans a saved filter set's name before it becomes a
// genre/tag value (Jellyfin's real BoxSet collection name is never run
// through this - only the flat-string tag substitute is). A preset name is
// free text an admin typed into the web UI - fine as-is for a real
// collection, which has no such constraints, but a genre/tag is a single
// flat string the Silo plugin treats as a label: an embedded newline/tab or
// run of whitespace would render as a garbled tag, and an unbounded length
// has no real upper limit enforced anywhere else in this pipeline. Collapses
// whitespace/control characters to single spaces, trims the ends, and caps
// the result at MaxTagNameRunes (counted in runes, not bytes, so a
// multi-byte name is never cut mid-character). Returns "" for a name that
// sanitizes away to nothing (for example one made only of control
// characters), which the caller treats as "skip this preset's tag" rather
// than emitting an empty genre.
func SanitizeTagName(name string) string {
	var b strings.Builder
	lastWasSpace := false
	for _, r := range name {
		if r == unicode.ReplacementChar {
			continue
		}
		if unicode.IsControl(r) {
			r = ' '
		}
		if unicode.IsSpace(r) {
			if lastWasSpace {
				continue
			}
			lastWasSpace = true
			r = ' '
		} else {
			lastWasSpace = false
		}
		b.WriteRune(r)
	}
	cleaned := strings.TrimSpace(b.String())
	if runes := []rune(cleaned); len(runes) > MaxTagNameRunes {
		cleaned = strings.TrimSpace(string(runes[:MaxTagNameRunes]))
	}
	return cleaned
}
