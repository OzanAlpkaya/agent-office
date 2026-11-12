package office

import (
	"embed"
	"fmt"
)

//go:embed presets/*.yaml
var presetFS embed.FS

// Preset is a layout shipped with agent-office, to start from.
type Preset struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	YAML        string `json:"yaml"`
}

var presetInfo = []struct{ name, title, description string }{
	{"classic", "Classic", "The default office as a layout: every room, 12 desks."},
	{"compact", "Compact", "A small team: no planning room, QA lab or review room; 6 desks."},
	{"open-plan", "Open plan", "Every room around one large workroom of 20 desks."},
	{"tall", "Tall", "Two narrow columns of rooms, so a phone shows the office larger; 6 desks."},
}

// Presets returns the shipped layouts, each valid.
func Presets() []Preset {
	out := make([]Preset, 0, len(presetInfo))
	for _, p := range presetInfo {
		b, err := presetFS.ReadFile("presets/" + p.name + ".yaml")
		if err != nil {
			panic(fmt.Sprintf("office: preset %s: %v", p.name, err)) // embedded at build time
		}
		out = append(out, Preset{Name: p.name, Title: p.title, Description: p.description, YAML: string(b)})
	}
	return out
}
