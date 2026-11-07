package github

import (
	"regexp"
	"strings"
)

// zwj is a zero-width joiner. Placed after '@' or inside a closing keyword, it
// stops GitHub from linking a mention or closing an issue, and is invisible.
const zwj = "‍"

var (
	mentionPattern = regexp.MustCompile(`@([A-Za-z0-9])`)
	closingPattern = regexp.MustCompile(`(?i)\b(c)(los(?:e|es|ed))\b|\b(f)(ix(?:es|ed)?)\b|\b(r)(esolv(?:e|es|ed))\b`)
)

// Neutralize makes agent-written text safe to post on GitHub (ADR 0020): an
// agent cannot @mention people (notifications) or use closing keywords
// ("fixes #12") that would close issues when a human merges.
func Neutralize(s string) string {
	s = mentionPattern.ReplaceAllString(s, "@"+zwj+"$1")
	return closingPattern.ReplaceAllStringFunc(s, func(m string) string {
		return m[:1] + zwj + m[1:]
	})
}

// Quote renders text as a Markdown block quote.
func Quote(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}
