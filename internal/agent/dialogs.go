package agent

import (
	"regexp"
	"strings"
	"unicode"
)

// First-run dialogs of Claude Code (Spike F, re-checked with 2.1.322 and
// 2.1.323): each blocks an interactive session before its SessionStart hook,
// with "No, exit" preselected where it asks. The pre-seeded config
// (SeedConfig, SandboxSettings) suppresses all of them; DetectDialog names the
// one a screen shows when a version no longer honours a key.

// Dialog is a known blocking screen and the seed key that suppresses it.
type Dialog struct {
	Name string // what the user sees
	Key  string // the pre-seeded key that should have skipped it
}

// dialogs are matched on the screen's text with escape sequences and
// whitespace removed (the TUI moves the cursor instead of printing spaces),
// lower case; the first match wins, so the specific ones come first.
var dialogs = []struct {
	Dialog
	phrases []string
}{
	{Dialog{"workspace trust dialog (\"Is this a project you created or one you trust?\")",
		`projects["/workspace"].hasTrustDialogAccepted in .claude.json`},
		[]string{"yes,itrustthisfolder", "isthisaprojectyoucreatedoroneyoutrust", "doyoutrustthefilesinthisfolder"}},
	{Dialog{"bypass-permissions warning (\"Claude Code running in Bypass Permissions mode\")",
		"skipDangerousModePermissionPrompt in the settings file"},
		[]string{"claudecoderunninginbypasspermissionsmode", "yes,iaccept"}},
	{Dialog{"theme picker (\"Choose the text style\")", "theme in the settings file"},
		[]string{"choosethetextstyle"}},
	{Dialog{"login screen (\"Select login method\")", "the credential in the environment (CLAUDE_CODE_OAUTH_TOKEN)"},
		[]string{"selectloginmethod"}},
	{Dialog{"welcome / onboarding screen (\"Welcome to Claude Code\")",
		"hasCompletedOnboarding and lastOnboardingVersion in .claude.json"},
		[]string{"welcometoclaudecode", "unabletoconnecttoanthropicservices"}},
}

var (
	csiRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	oscRe = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
	escRe = regexp.MustCompile(`\x1b[()*+][0-9A-Za-z]|\x1b[@-_=>78]`) // charset selection, keypad modes, save/restore
)

// ScreenText is a terminal stream as plain text: escape sequences removed,
// cursor moves as spaces.
func ScreenText(stream []byte) string {
	s := oscRe.ReplaceAllString(string(stream), "")
	s = csiRe.ReplaceAllString(s, " ")
	return escRe.ReplaceAllString(s, "")
}

// DetectDialog names the first-run dialog a terminal stream shows; ok is
// false when it shows none of them.
func DetectDialog(stream []byte) (Dialog, bool) {
	var b strings.Builder
	for _, r := range strings.ToLower(ScreenText(stream)) {
		if !unicode.IsSpace(r) && unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	flat := b.String()
	for _, d := range dialogs {
		for _, p := range d.phrases {
			if strings.Contains(flat, p) {
				return d.Dialog, true
			}
		}
	}
	return Dialog{}, false
}
