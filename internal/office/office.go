// Package office parses and validates a project's office layout (Phase 18):
// which rooms the pixel office has, where they sit on a grid of tiles, how
// many desks the workroom has, and which rooms open into each other. The UI
// draws the floor plan from the parsed layout; it never parses YAML.
//
// The geometry rules that need world pixels (a room's back wall, the size of
// a desk) mirror the UI's floor plan code (web/src/office/custom.ts); the
// presets' parsed form is checked in as web/src/office/presets.gen.json so
// both sides are tested against the same layouts.
package office

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// Limits and world-pixel sizes (the UI's floor plan uses the same).
const (
	MaxBytes   = 8192 // the YAML of one layout
	MaxColumns = 64   // tiles
	MaxRows    = 48
	MaxDesks   = 32 // a project's agent cap (engine.MaxProjectAgents)
	maxName    = 24 // a room's own name

	Tile     = 16 // world pixels per tile
	face     = 44 // a room's back wall, seen from the front
	wall     = 4  // a wall seen from above, at the bottom of every room
	cellW    = 64 // a desk cell
	cellH    = 52
	margin   = 16 // the workroom's side margins
	corridor = 14 // free floor between the workroom's back wall and its first row of desks
	bottom   = 10 // free floor under the last row
	doorway  = 20 // a doorway's width
	doorEdge = 10 // floor kept on each side of a doorway along a back wall
)

// Room ids. Required rooms must be in every layout.
const (
	Lead     = "lead"
	Planning = "planning"
	User     = "user"
	Review   = "review"
	QA       = "qa"
	Kitchen  = "kitchen"
	Lobby    = "lobby"
	Work     = "work"
)

// RoomInfo is what a room needs: whether a layout must have it, and its
// smallest size in tiles (its furniture must fit).
type RoomInfo struct {
	Required bool
	MinW     int
	MinH     int
	Purpose  string
}

// Rooms are the rooms a layout may have.
var Rooms = map[string]RoomInfo{
	Lead:     {Required: true, MinW: 8, MinH: 7, Purpose: "the Tech Lead's desk and whiteboard"},
	User:     {Required: true, MinW: 6, MinH: 7, Purpose: "your office; agents waiting on you queue at its door"},
	Lobby:    {Required: true, MinW: 7, MinH: 7, Purpose: "the front door and the assistant's reception"},
	Work:     {Required: true, MinW: 6, MinH: 7, Purpose: "every other agent's desk"},
	Planning: {MinW: 6, MinH: 7, Purpose: "the architect's drafting table; without it the architect sits in the workroom"},
	QA:       {MinW: 6, MinH: 7, Purpose: "QA's test bench; without it QA sits in the workroom"},
	Review:   {MinW: 7, MinH: 7, Purpose: "the reviewers' table; without it reviewers stay at their desks"},
	Kitchen:  {MinW: 5, MinH: 7, Purpose: "coffee for idle agents; without it there are no coffee breaks"},
}

// roomOrder is the order problems and the parsed layout list rooms in when
// the YAML does not decide it.
var roomOrder = []string{Lead, Planning, User, Review, QA, Kitchen, Lobby, Work}

// Room is one room on the grid, in tiles.
type Room struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"` // shown instead of the room's usual name
	X    int    `json:"x"`
	Y    int    `json:"y"`
	W    int    `json:"w"`
	H    int    `json:"h"`
}

// Door joins two rooms through the middle of their shared wall.
type Door struct {
	A string `json:"a"`
	B string `json:"b"`
}

// Layout is a parsed, valid layout.
type Layout struct {
	Columns int    `json:"columns"` // the grid's size in tiles: the rooms' bounding box
	Rows    int    `json:"rows"`
	Rooms   []Room `json:"rooms"`
	Doors   []Door `json:"doors"`
	Desks   int    `json:"desks"` // regular desks in the workroom
}

// Error lists every problem of a layout, each with where it is.
type Error struct {
	Problems []string
}

func (e *Error) Error() string {
	if len(e.Problems) == 1 {
		return "office layout: " + e.Problems[0]
	}
	return fmt.Sprintf("office layout: %d problems: %s", len(e.Problems), strings.Join(e.Problems, "; "))
}

// file is the YAML form.
type file struct {
	Version int        `yaml:"version"`
	Rooms   []fileRoom `yaml:"rooms"`
	Doors   [][]string `yaml:"doors"`
}

type fileRoom struct {
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	X     *int   `yaml:"x"`
	Y     *int   `yaml:"y"`
	W     *int   `yaml:"w"`
	H     *int   `yaml:"h"`
	Desks *int   `yaml:"desks"`
}

var nameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} '&.-]*$`)

// Parse reads and validates a layout. A layout with problems returns an
// *Error listing all of them.
func Parse(src string) (Layout, error) {
	var probs []string
	add := func(format string, a ...any) { probs = append(probs, fmt.Sprintf(format, a...)) }
	fail := func() (Layout, error) { return Layout{}, &Error{Problems: probs} }

	if len(src) > MaxBytes {
		add("it is %d bytes; at most %d", len(src), MaxBytes)
		return fail()
	}
	if !utf8.ValidString(src) {
		add("it is not UTF-8 text")
		return fail()
	}
	var f file
	dec := yaml.NewDecoder(bytes.NewReader([]byte(src)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			add("it is empty; start with `version: 1` and a list of rooms")
		} else {
			add("%s", yamlProblem(err))
		}
		return fail()
	}
	var more any
	if err := dec.Decode(&more); !errors.Is(err, io.EOF) {
		add("more than one YAML document (a second ---)")
		return fail()
	}
	if f.Version != 1 {
		add("version: %d is not supported; use version: 1", f.Version)
	}

	l := Layout{Rooms: []Room{}, Doors: []Door{}}
	seen := map[string]bool{}
	desks, desksGiven := 0, false
	for i, fr := range f.Rooms {
		at := fmt.Sprintf("rooms[%d]", i)
		if fr.ID != "" {
			at = fmt.Sprintf("room %s", fr.ID)
		}
		info, known := Rooms[fr.ID]
		switch {
		case fr.ID == "":
			add("%s: id is missing (one of %s)", at, strings.Join(roomOrder, ", "))
			continue
		case !known:
			add("%s: unknown room; the rooms are %s", at, strings.Join(roomOrder, ", "))
			continue
		case seen[fr.ID]:
			add("%s: listed twice", at)
			continue
		}
		seen[fr.ID] = true
		r := Room{ID: fr.ID}
		missing := []string{}
		for _, p := range []struct {
			name string
			v    *int
			dst  *int
		}{{"x", fr.X, &r.X}, {"y", fr.Y, &r.Y}, {"w", fr.W, &r.W}, {"h", fr.H, &r.H}} {
			if p.v == nil {
				missing = append(missing, p.name)
				continue
			}
			*p.dst = *p.v
		}
		if len(missing) > 0 {
			add("%s: %s missing (whole tiles)", at, strings.Join(missing, ", "))
			continue
		}
		if fr.Name != "" {
			n := strings.TrimSpace(fr.Name)
			if utf8.RuneCountInString(n) > maxName || !nameRe.MatchString(n) {
				add("%s: name %q: at most %d letters, digits, spaces and ' & . -", at, fr.Name, maxName)
			} else {
				r.Name = n
			}
		}
		if fr.Desks != nil {
			desksGiven = fr.ID == Work
			if fr.ID != Work {
				add("%s: only the workroom has desks", at)
			} else if *fr.Desks < 1 || *fr.Desks > MaxDesks {
				add("%s: desks: %d; between 1 and %d", at, *fr.Desks, MaxDesks)
			} else {
				desks = *fr.Desks
			}
		}
		// Each value on its own first, so the sums below cannot overflow (security review).
		if r.X < 0 || r.Y < 0 || r.W < 1 || r.H < 1 || r.X > MaxColumns || r.Y > MaxRows || r.W > MaxColumns || r.H > MaxRows {
			add("%s: x, y, w and h must be whole tiles inside a grid of %d × %d (x and y from 0)", at, MaxColumns, MaxRows)
			continue
		}
		if r.X+r.W > MaxColumns || r.Y+r.H > MaxRows {
			add("%s: it must lie inside a grid of %d × %d tiles (x and y from 0)", at, MaxColumns, MaxRows)
		}
		if r.W < info.MinW || r.H < info.MinH {
			add("%s: %d × %d tiles is too small for %s; at least %d × %d", at, r.W, r.H, info.Purpose, info.MinW, info.MinH)
		}
		l.Rooms = append(l.Rooms, r)
	}
	for _, id := range roomOrder {
		if Rooms[id].Required && !seen[id] {
			add("room %s is missing: every office has %s", id, Rooms[id].Purpose)
		}
	}
	if len(l.Rooms) == 0 {
		return fail()
	}

	// The grid is the rooms' bounding box from (0, 0).
	for _, r := range l.Rooms {
		l.Columns = max(l.Columns, r.X+r.W)
		l.Rows = max(l.Rows, r.Y+r.H)
	}
	for i, a := range l.Rooms {
		for _, b := range l.Rooms[i+1:] {
			if a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
				add("rooms %s and %s overlap", a.ID, b.ID)
			}
		}
	}

	// Desks: the workroom's, which must fit it.
	if w, ok := l.room(Work); ok {
		if !desksGiven {
			add("room work: desks is missing (how many desks the workroom has, 1 to %d)", MaxDesks)
		} else if fit := DeskCapacity(w); desks > fit {
			add("room work: %d desks do not fit in %d × %d tiles; at most %d", desks, w.W, w.H, fit)
		}
		l.Desks = desks
	}
	// Doors and reachability are checked once the rooms are right: a room
	// that is missing or misplaced would only add confusing problems.
	if len(probs) > 0 {
		return fail()
	}

	// Doors.
	pairs := map[[2]string]bool{}
	for i, d := range f.Doors {
		at := fmt.Sprintf("doors[%d]", i)
		if len(d) != 2 {
			add("%s: a door joins two rooms, e.g. [lead, work]", at)
			continue
		}
		a, b := d[0], d[1]
		ra, okA := l.room(a)
		rb, okB := l.room(b)
		switch {
		case a == b:
			add("%s: [%s, %s] joins a room to itself", at, a, b)
			continue
		case !okA || !okB:
			unknown := a
			if okA {
				unknown = b
			}
			add("%s: [%s, %s]: there is no room %s in this layout", at, a, b, unknown)
			continue
		}
		key := [2]string{min(a, b), max(a, b)}
		if pairs[key] {
			add("%s: [%s, %s] is listed twice", at, a, b)
			continue
		}
		pairs[key] = true
		if _, ok := doorSpan(ra, rb); !ok {
			add("%s: [%s, %s]: the rooms must share a wall long enough for a doorway (3 tiles along a back wall, 2 beside it)", at, a, b)
			continue
		}
		l.Doors = append(l.Doors, Door{A: a, B: b})
	}

	// Every room is reachable from the lobby, and the user's office has a door.
	if _, ok := l.room(User); ok && !slices.ContainsFunc(l.Doors, func(d Door) bool { return d.A == User || d.B == User }) {
		add("room user has no door: agents waiting on you queue at it")
	}
	if _, ok := l.room(Lobby); ok {
		reached := map[string]bool{Lobby: true}
		queue := []string{Lobby}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, d := range l.Doors {
				next := ""
				if d.A == cur {
					next = d.B
				} else if d.B == cur {
					next = d.A
				}
				if next != "" && !reached[next] {
					reached[next] = true
					queue = append(queue, next)
				}
			}
		}
		for _, r := range l.Rooms {
			if !reached[r.ID] {
				add("room %s cannot be reached from the lobby through doors", r.ID)
			}
		}
	}
	if len(probs) > 0 {
		return fail()
	}
	return l, nil
}

func (l Layout) room(id string) (Room, bool) {
	for _, r := range l.Rooms {
		if r.ID == id {
			return r, true
		}
	}
	return Room{}, false
}

// HasRoom reports whether the layout has a room.
func (l Layout) HasRoom(id string) bool {
	_, ok := l.room(id)
	return ok
}

// DeskCapacity is how many desks fit a workroom: whole columns of desk cells
// between its side margins, and rows below its back wall.
func DeskCapacity(w Room) int {
	cols := (w.W*Tile - 2*margin) / cellW
	floorH := w.H*Tile - face - wall
	rows := (floorH - corridor - bottom) / cellH
	if cols <= 0 || rows <= 0 {
		return 0
	}
	return cols * rows
}

// doorSpan is where a door between a and b can go, in world pixels along
// their shared wall: a back wall (one above the other) needs room for the
// doorway and floor beside it; a side wall needs the doorway's height of
// floor both rooms share.
func doorSpan(a, b Room) ([2]int, bool) {
	px := func(t int) int { return t * Tile }
	if a.Y+a.H == b.Y || b.Y+b.H == a.Y { // one above the other
		lo := max(px(a.X), px(b.X)) + doorEdge
		hi := min(px(a.X+a.W), px(b.X+b.W)) - doorEdge
		return [2]int{lo, hi}, hi-lo >= doorway+2*doorEdge
	}
	if a.X+a.W == b.X || b.X+b.W == a.X { // side by side
		lo := max(px(a.Y), px(b.Y)) + face
		hi := min(px(a.Y+a.H), px(b.Y+b.H)) - wall
		return [2]int{lo, hi}, hi-lo >= doorway+8
	}
	return [2]int{}, false
}

// yamlProblem shortens a YAML error to its useful part.
func yamlProblem(err error) string {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		return strings.Join(te.Errors, "; ")
	}
	return strings.TrimPrefix(err.Error(), "yaml: ")
}

// Problems returns an error's problems, or its text.
func Problems(err error) []string {
	var e *Error
	if errors.As(err, &e) {
		return e.Problems
	}
	if err == nil {
		return nil
	}
	return []string{err.Error()}
}
