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
	imagePattern   = regexp.MustCompile(`!\[`)
	closingPattern = regexp.MustCompile(`(?i)\b(c)(los(?:e|es|ed))\b|\b(f)(ix(?:es|ed)?)\b|\b(r)(esolv(?:e|es|ed))\b`)
	// closingRefPattern is GitHub's closing syntax: a keyword, an optional
	// colon, then an issue as #N, owner/repo#N or an issue URL.
	closingRefPattern = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b:?[ \t]*` +
		`(#\d+|[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#\d+|https?://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/issues/\d+)`)
)

// ClosingReferences returns the issue-closing references in a commit message
// ("fixes #12"), in order. GitHub closes those issues when a commit carrying
// them reaches the default branch, whatever the merge message says, so the
// daemon flags such commits before pushing them (ADR 0020).
func ClosingReferences(msg string) []string {
	var refs []string
	for _, m := range closingRefPattern.FindAllString(msg, 20) {
		refs = append(refs, m)
	}
	return refs
}

// Neutralize makes agent-written text safe to post on GitHub (ADR 0020): an
// agent cannot @mention people (notifications), use closing keywords
// ("fixes #12") that would close issues when a human merges, or embed images
// that GitHub would fetch (a way to send data to a URL); an image becomes a
// plain link.
func Neutralize(s string) string {
	s = mentionPattern.ReplaceAllString(s, "@"+zwj+"$1")
	s = imagePattern.ReplaceAllString(s, "!"+zwj+"[")
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
