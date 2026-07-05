package mempalace

import (
	"encoding/json"
	"sort"
	"strings"
)

// TechnicalRoomPenalty demotes generic transcript/boilerplate hits (room
// "technical", or any source_file that's a raw mined transcript) so they
// don't outrank a genuinely relevant fact just because a task-description
// echo or a raw tool-call dump happens to share more surface vocabulary with
// the query. SupersededPenalty demotes drawers marked as no-longer-true (the
// [SUPERSEDED prefix — set by memory_invalidate and by the engine's
// supersedePreviousPlan) — verbatim history stays recallable, it just
// shouldn't rank above current truth for a general query. Both are
// multiplicative on mempalace's own similarity score, applied once each
// (never compounded).
const (
	TechnicalRoomPenalty = 0.55
	SupersededPenalty    = 0.15
)

// RerankSearchResults re-ranks and trims a mempalace_search JSON response.
// Mempalace's own ranking is purely similarity/BM25 — it has no notion of
// "this is boilerplate" or "this was superseded" — so callers who over-fetch
// (the recall_memory/recall_company agent tools, and the /api/memory/search
// HTTP endpoint) apply these penalties locally and truncate to the
// originally-requested limit. Unknown fields on results and other top-level
// keys (query, filters, total_before_filter) pass through untouched; on any
// parse failure the input is returned as-is so a caller never gets a worse
// result than not re-ranking at all. Sort is stable, so results with equal
// adjusted scores keep mempalace's original relative order.
func RerankSearchResults(raw string, limit int) string {
	var parsed struct {
		Results []map[string]any `json:"results"`
	}
	var top map[string]any
	if json.Unmarshal([]byte(raw), &top) != nil {
		return raw
	}
	if json.Unmarshal([]byte(raw), &parsed) != nil {
		return raw
	}
	if len(parsed.Results) == 0 {
		return raw
	}

	type scored struct {
		result map[string]any
		score  float64
	}
	items := make([]scored, len(parsed.Results))
	for i, r := range parsed.Results {
		sim, _ := r["similarity"].(float64)
		text, _ := r["text"].(string)
		room, _ := r["room"].(string)
		sourceFile, _ := r["source_file"].(string)

		score := sim
		if room == "technical" || strings.HasPrefix(sourceFile, "transcript") {
			score *= TechnicalRoomPenalty
		}
		if strings.HasPrefix(text, "[SUPERSEDED") || strings.HasPrefix(text, "[superseded]") {
			score *= SupersededPenalty
		}
		items[i] = scored{result: r, score: score}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].score > items[j].score })
	if len(items) > limit {
		items = items[:limit]
	}
	results := make([]map[string]any, len(items))
	for i, it := range items {
		results[i] = it.result
	}
	top["results"] = results
	out, err := json.Marshal(top)
	if err != nil {
		return raw
	}
	return string(out)
}
