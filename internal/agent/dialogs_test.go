package agent

import (
	"strings"
	"testing"
)

// Screens as Claude Code 2.1.323 draws them (Spike F, 2026-11-13), rebuilt
// from their text: cursor moves between words instead of spaces, colours,
// the window title.
func tuiScreen(lines ...string) []byte {
	var b strings.Builder
	b.WriteString("\x1b]0;claude\x07\x1b[?25l\x1b[2J")
	for i, l := range lines {
		b.WriteString("\x1b[" + string(rune('1'+i%9)) + ";1H\x1b[38;5;174m")
		b.WriteString(strings.ReplaceAll(l, " ", "\x1b[1C"))
		b.WriteString("\x1b[39m")
	}
	return []byte(b.String())
}

func TestDetectDialog(t *testing.T) {
	for name, c := range map[string]struct {
		screen []byte
		want   string // part of the dialog's name; "" for none
	}{
		"trust": {tuiScreen("Accessing workspace:", "/workspace", "Quick safety check: Is this a project you created or one you trust?",
			"❯ No, exit", "Yes, I trust this folder"), "workspace trust"},
		"bypass":  {tuiScreen("WARNING: Claude Code running in Bypass Permissions mode", "❯ No, exit", "Yes, I accept"), "bypass-permissions"},
		"welcome": {tuiScreen("Welcome to Claude Code v2.1.323", "Unable to connect to Anthropic services"), "welcome"},
		"theme":   {tuiScreen("Choose the text style that looks best with your terminal"), "theme"},
		"login":   {tuiScreen("Select login method:"), "login"},
		"prompt": {tuiScreen("Claude Code v2.1.323", "Haiku 5.5 · Claude API", "/workspace", "❯ ",
			"⏵⏵ bypass permissions on (shift+tab to cycle)"), ""},
		"empty": {nil, ""},
	} {
		d, ok := DetectDialog(c.screen)
		if c.want == "" {
			if ok {
				t.Errorf("%s: detected %q on a screen without a dialog", name, d.Name)
			}
			continue
		}
		if !ok || !strings.Contains(d.Name, c.want) || d.Key == "" {
			t.Errorf("%s: %+v %v, want %q", name, d, ok, c.want)
		}
	}
}

func TestScreenText(t *testing.T) {
	if got := ScreenText([]byte("\x1b]0;t\x07a\x1b[1Cb\x1b[0m\x1b(Bc")); got != "a b c" {
		t.Fatalf("%q", got)
	}
}
