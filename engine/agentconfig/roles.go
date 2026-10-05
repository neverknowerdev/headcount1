package agentconfig

import (
	"strings"
	"unicode"
)

// CanonicalRole normalizes built-in role labels accepted by the UI. The UI
// commonly displays a built-in agent as "CEO Agent", while older rows store
// either "CEO" or an empty role key.
func CanonicalRole(value string) string {
	var words []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			words = append(words, strings.ToLower(string(current)))
			current = nil
		}
	}
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current = append(current, r)
			continue
		}
		flush()
	}
	flush()
	if len(words) > 1 && words[len(words)-1] == "agent" {
		words = words[:len(words)-1]
	}
	switch strings.Join(words, " ") {
	case "ceo", "chief executive officer":
		return "CEO"
	case "cto", "chief technology officer":
		return "CTO"
	case "cmo", "chief marketing officer":
		return "CMO"
	case "coder":
		return "Coder"
	case "qa", "quality assurance":
		return "QA"
	default:
		return strings.TrimSpace(value)
	}
}

// RoleMatches reports whether either the persisted role key or display name
// identifies the requested role. Name fallback supports legacy and manually
// created built-in agents whose role key was left as "CEO Agent" or blank.
func RoleMatches(roleKey, name, requested string) bool {
	want := CanonicalRole(requested)
	return strings.EqualFold(CanonicalRole(roleKey), want) ||
		strings.EqualFold(CanonicalRole(name), want)
}
