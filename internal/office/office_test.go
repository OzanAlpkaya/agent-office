package office

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite web/src/office/presets.gen.json")

const minimal = `version: 1
rooms:
  - { id: lead,  x: 0, y: 0, w: 8, h: 7 }
  - { id: user,  x: 8, y: 0, w: 7, h: 7 }
  - { id: lobby, x: 8, y: 7, w: 7, h: 7 }
  - { id: work,  x: 0, y: 7, w: 8, h: 11, desks: 2 }
doors:
  - [user, lobby]
  - [lobby, work]
  - [lead, work]
`

func TestParseValid(t *testing.T) {
	l, err := Parse(minimal)
	if err != nil {
		t.Fatal(err)
	}
	if l.Columns != 15 || l.Rows != 18 || l.Desks != 2 || len(l.Rooms) != 4 || len(l.Doors) != 3 {
		t.Fatalf("layout = %+v", l)
	}
	if l.HasRoom(QA) || !l.HasRoom(Lobby) {
		t.Fatal("HasRoom")
	}
}

func TestPresetsAreValid(t *testing.T) {
	ps := Presets()
	if len(ps) != 4 {
		t.Fatalf("presets = %d", len(ps))
	}
	parsed := map[string]Layout{}
	for _, p := range ps {
		l, err := Parse(p.YAML)
		if err != nil {
			t.Errorf("preset %s: %v", p.Name, err)
			continue
		}
		parsed[p.Name] = l
	}
	if parsed["compact"].HasRoom(QA) || !parsed["classic"].HasRoom(Review) || parsed["open-plan"].Desks != 20 {
		t.Fatalf("presets: %+v", parsed)
	}
	// The UI's tests build a floor plan from each preset and walk it.
	b, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	golden := filepath.Join("..", "..", "web", "src", "office", "presets.gen.json")
	if *update {
		if err := os.WriteFile(golden, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (go test ./internal/office -update writes it)", err)
	}
	if string(want) != string(b) {
		t.Fatalf("%s is out of date: go test ./internal/office -update", golden)
	}
}

func TestParseInvalid(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		want []string // substrings, one per expected problem
	}{
		"empty":         {"", []string{"it is empty"}},
		"not yaml":      {"rooms: [", []string{"did not find"}},
		"unknown field": {"version: 1\nfloors: 2\n", []string{"field floors not found"}},
		"two documents": {minimal + "---\nversion: 1\n", []string{"more than one YAML document"}},
		"too large":     {strings.Repeat("#", MaxBytes+1), []string{"at most 8192"}},
		"version":       {strings.Replace(minimal, "version: 1", "version: 2", 1), []string{"version: 2 is not supported"}},
		"required rooms missing": {"version: 1\nrooms:\n  - { id: work, x: 0, y: 0, w: 8, h: 11, desks: 2 }\n",
			[]string{"room lead is missing", "room user is missing", "room lobby is missing"}},
		"unknown and duplicate room": {strings.Replace(minimal, "doors:", "  - { id: gym, x: 20, y: 0, w: 6, h: 7 }\n  - { id: user, x: 30, y: 0, w: 7, h: 7 }\ndoors:", 1),
			[]string{"room gym: unknown room", "room user: listed twice"}},
		"too small": {strings.Replace(minimal, "{ id: lead,  x: 0, y: 0, w: 8, h: 7 }", "{ id: lead,  x: 0, y: 0, w: 8, h: 5 }", 1),
			[]string{"room lead: 8 × 5 tiles is too small"}},
		"outside the grid": {strings.Replace(minimal, "{ id: user,  x: 8, y: 0, w: 7, h: 7 }", "{ id: user,  x: 60, y: 0, w: 7, h: 7 }", 1),
			[]string{"room user: it must lie inside a grid of 64 × 48"}},
		"huge coordinates": {strings.Replace(minimal, "{ id: user,  x: 8, y: 0, w: 7, h: 7 }", "{ id: user,  x: 9223372036854775807, y: 0, w: 8, h: 7 }", 1),
			[]string{"room user: x, y, w and h must be whole tiles inside a grid of 64 × 48"}},
		"zero size": {strings.Replace(minimal, "{ id: user,  x: 8, y: 0, w: 7, h: 7 }", "{ id: user,  x: 8, y: 0, w: 0, h: 7 }", 1),
			[]string{"room user: x, y, w and h must be whole tiles"}},
		"missing size": {strings.Replace(minimal, "{ id: user,  x: 8, y: 0, w: 7, h: 7 }", "{ id: user,  x: 8, y: 0 }", 1),
			[]string{"room user: w, h missing"}},
		"overlap": {strings.Replace(minimal, "{ id: lobby, x: 8, y: 7, w: 7, h: 7 }", "{ id: lobby, x: 6, y: 7, w: 7, h: 7 }", 1),
			[]string{"rooms lobby and work overlap"}},
		"desks missing":      {strings.Replace(minimal, ", desks: 2", "", 1), []string{"room work: desks is missing"}},
		"desks do not fit":   {strings.Replace(minimal, "desks: 2", "desks: 9", 1), []string{"9 desks do not fit in 8 × 11 tiles; at most 2"}},
		"desks out of range": {strings.Replace(minimal, "desks: 2", "desks: 40", 1), []string{"desks: 40; between 1 and 32"}},
		"desks elsewhere": {strings.Replace(minimal, "{ id: lead,  x: 0, y: 0, w: 8, h: 7 }", "{ id: lead,  x: 0, y: 0, w: 8, h: 7, desks: 1 }", 1),
			[]string{"room lead: only the workroom has desks"}},
		"bad name": {strings.Replace(minimal, "{ id: lead,  x: 0, y: 0, w: 8, h: 7 }", "{ id: lead,  x: 0, y: 0, w: 8, h: 7, name: \"<b>x</b>\" }", 1),
			[]string{"room lead: name"}},
		"door to a missing room":     {minimal + "  - [qa, work]\n", []string{"doors[3]: [qa, work]: there is no room qa"}},
		"door without a shared wall": {minimal + "  - [lead, lobby]\n", []string{"doors[3]: [lead, lobby]: the rooms must share a wall"}},
		"door twice":                 {minimal + "  - [work, lobby]\n", []string{"doors[3]: [work, lobby] is listed twice"}},
		"door to itself":             {minimal + "  - [work, work]\n", []string{"joins a room to itself"}},
		"door of one room":           {minimal + "  - [work]\n", []string{"a door joins two rooms"}},
		"unreachable":                {strings.Replace(minimal, "  - [lead, work]\n", "", 1), []string{"room lead cannot be reached from the lobby"}},
		"user without a door": {strings.Replace(minimal, "  - [user, lobby]\n", "", 1),
			[]string{"room user has no door", "room user cannot be reached"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(tc.src)
			probs := Problems(err)
			if len(probs) != len(tc.want) {
				t.Fatalf("problems = %q, want %d like %q", probs, len(tc.want), tc.want)
			}
			for _, w := range tc.want {
				found := false
				for _, p := range probs {
					found = found || strings.Contains(p, w)
				}
				if !found {
					t.Errorf("no problem contains %q: %q", w, probs)
				}
			}
		})
	}
}

func TestDeskCapacity(t *testing.T) {
	for _, tc := range []struct {
		w, h, want int
	}{
		{6, 7, 0},    // a back wall and too little floor
		{6, 8, 1},    // one desk cell between the margins, one row
		{26, 11, 12}, // six columns, two rows
		{32, 15, 21},
	} {
		if got := DeskCapacity(Room{ID: Work, W: tc.w, H: tc.h}); got != tc.want {
			t.Errorf("DeskCapacity(%d × %d) = %d, want %d", tc.w, tc.h, got, tc.want)
		}
	}
}
