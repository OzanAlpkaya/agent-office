package git

import (
	"strings"
	"unicode"
)

// BranchPrefix namespaces every branch agent-office creates.
const BranchPrefix = "agent-office/"

// MaxSlugLen bounds the task part of a branch name.
const MaxSlugLen = 40

// shortIDLen is how much of the agent id goes into task branch names.
const shortIDLen = 6

// translit maps non-ASCII letters to ASCII. It must be applied before
// lowercasing: strings.ToLower("İ") is "i" plus a combining dot, not "i".
var translit = map[rune]string{
	'ş': "s", 'Ş': "s", 'ğ': "g", 'Ğ': "g", 'ı': "i", 'İ': "i",
	'ö': "o", 'Ö': "o", 'ü': "u", 'Ü': "u", 'ç': "c", 'Ç': "c",
	// A few common Latin letters beyond Turkish.
	'ä': "a", 'Ä': "a", 'é': "e", 'É': "e", 'è': "e", 'ê': "e", 'á': "a", 'à': "a", 'â': "a",
	'í': "i", 'î': "i", 'ó': "o", 'ô': "o", 'ú': "u", 'û': "u", 'ñ': "n", 'Ñ': "n", 'ß': "ss",
}

// Slug turns free text into an ASCII slug: lowercase letters and digits
// separated by single hyphens, at most max bytes, never starting or ending with
// a hyphen. Letters it cannot transliterate are treated as separators.
func Slug(s string, max int) string {
	var b strings.Builder
	sep := false
	for _, r := range s {
		if t, ok := translit[r]; ok {
			if sep && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteString(t)
			sep = false
			continue
		}
		r = unicode.ToLower(r)
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if sep && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			sep = false
			continue
		}
		sep = true
	}
	out := b.String()
	if len(out) > max {
		out = out[:max]
		if i := strings.LastIndexByte(out, '-'); i > max/2 { // prefer cutting at a word boundary
			out = out[:i]
		}
	}
	return strings.Trim(out, "-")
}

// BranchName returns the branch for an agent: agent-office/<task-slug>-<short-id>
// when it works on a task whose title yields a slug, agent-office/<agent-id> otherwise.
func BranchName(taskTitle, agentID string) string {
	if slug := Slug(taskTitle, MaxSlugLen); slug != "" {
		short := agentID
		if len(short) > shortIDLen {
			short = short[:shortIDLen]
		}
		return BranchPrefix + slug + "-" + short
	}
	return BranchPrefix + agentID
}
