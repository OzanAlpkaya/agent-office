package office

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Generated layouts (Phase 20): random but valid offices, so the UI's tests
// check its floor plans (windows on outside walls only) on more than the
// presets. Rooms are laid out in bands of random order, widths and heights,
// with the workroom in any band; bands are as wide as the widest, or not,
// so some back walls are partly outside. Doors form a random spanning tree
// of the rooms that share a wall, plus a few more. Each one passes Parse.

const generatedCount = 40

func generateLayouts(seed uint64, n int) ([]Layout, error) {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	var out []Layout
	for attempt := 0; len(out) < n; attempt++ {
		if attempt > 50*n {
			return nil, fmt.Errorf("only %d of %d layouts in %d attempts", len(out), n, attempt)
		}
		if l, err := Parse(randomLayout(rng)); err == nil {
			out = append(out, l)
		}
	}
	return out, nil
}

type genRoom struct {
	id         string
	x, y, w, h int
	band, pos  int
}

func randomLayout(rng *rand.Rand) string {
	ids := []string{Lead, User, Lobby}
	for _, id := range []string{Planning, QA, Review, Kitchen} {
		if rng.IntN(10) < 6 {
			ids = append(ids, id)
		}
	}
	rng.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	// 2 or 3 bands of rooms, then the workroom as a band of its own somewhere.
	nb := 2 + rng.IntN(2)
	if len(ids) < 2*nb {
		nb = 2
	}
	var bands [][]string
	for i, per := 0, (len(ids)+nb-1)/nb; i < len(ids); i += per {
		bands = append(bands, ids[i:min(i+per, len(ids))])
	}
	workBand := rng.IntN(len(bands) + 1)
	bands = slices.Insert(bands, workBand, []string{Work})

	widths := make([]int, len(bands))
	var rooms []genRoom
	y := 0
	for b, band := range bands {
		h := 7 + rng.IntN(3)
		if band[0] == Work {
			h = 9 + rng.IntN(6)
		}
		x := 0
		for i, id := range band {
			w := Rooms[id].MinW + rng.IntN(5)
			if id == Work {
				w = 12 + rng.IntN(16)
			}
			rooms = append(rooms, genRoom{id: id, x: x, y: y, w: w, h: h, band: b, pos: i})
			x += w
		}
		widths[b] = x
		y += h
	}
	W := slices.Max(widths)
	if rng.IntN(2) == 0 { // every band as wide as the widest: no outside stretch below the top band
		for i := range rooms {
			r := &rooms[i]
			if r.pos == len(bands[r.band])-1 {
				r.w += W - widths[r.band]
			}
		}
	}

	// Doors: rooms that share a wall long enough, a random spanning tree first.
	type pair struct{ a, b int }
	var cand []pair
	for i := range rooms {
		for j := i + 1; j < len(rooms); j++ {
			a, b := rooms[i], rooms[j]
			switch {
			case a.band == b.band && (a.pos+1 == b.pos || b.pos+1 == a.pos):
				cand = append(cand, pair{i, j})
			case a.band+1 == b.band || b.band+1 == a.band:
				if min(a.x+a.w, b.x+b.w)-max(a.x, b.x) >= 4 {
					cand = append(cand, pair{i, j})
				}
			}
		}
	}
	rng.Shuffle(len(cand), func(i, j int) { cand[i], cand[j] = cand[j], cand[i] })
	parent := make([]int, len(rooms))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	var doors []pair
	for _, c := range cand {
		if ra, rb := find(c.a), find(c.b); ra != rb {
			parent[ra] = rb
			doors = append(doors, c)
		} else if rng.IntN(4) == 0 {
			doors = append(doors, c)
		}
	}

	var b strings.Builder
	b.WriteString("version: 1\nrooms:\n")
	for _, r := range rooms {
		fmt.Fprintf(&b, "  - { id: %s, x: %d, y: %d, w: %d, h: %d", r.id, r.x, r.y, r.w, r.h)
		if r.id == Work {
			fit := DeskCapacity(Room{ID: Work, W: r.w, H: r.h})
			fmt.Fprintf(&b, ", desks: %d", 1+rng.IntN(max(1, min(fit, MaxDesks))))
		}
		b.WriteString(" }\n")
	}
	b.WriteString("doors:\n")
	for _, d := range doors {
		fmt.Fprintf(&b, "  - [%s, %s]\n", rooms[d.a].id, rooms[d.b].id)
	}
	return b.String()
}

// TestGeneratedLayouts writes the generated layouts for the UI's tests
// (web/src/office/generated.gen.json); -update rewrites it.
func TestGeneratedLayouts(t *testing.T) {
	ls, err := generateLayouts(20, generatedCount)
	if err != nil {
		t.Fatal(err)
	}
	// Variety the UI's tests rely on: the workroom not only at the bottom, optional rooms
	// missing in some, and back walls with a room behind them only in part.
	workNotLast, missing, partial := 0, 0, 0
	for _, l := range ls {
		if len(l.Rooms) < len(Rooms) {
			missing++
		}
		w, _ := l.room(Work)
		if w.Y+w.H < l.Rows {
			workNotLast++
		}
		if partlyOutside(l) {
			partial++
		}
	}
	if workNotLast == 0 || missing == 0 || partial == 0 {
		t.Fatalf("too little variety: workroom not at the bottom %d, rooms missing %d, partly outside back walls %d",
			workNotLast, missing, partial)
	}
	named := map[string]Layout{}
	for i, l := range ls {
		named[fmt.Sprintf("generated-%02d", i)] = l
	}
	b, err := json.MarshalIndent(named, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	golden := filepath.Join("..", "..", "web", "src", "office", "generated.gen.json")
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

// partlyOutside reports whether some room's back wall has a room behind one
// part and nothing behind another.
func partlyOutside(l Layout) bool {
	for _, r := range l.Rooms {
		if r.Y == 0 {
			continue
		}
		covered := 0
		for c := r.X; c < r.X+r.W; c++ {
			for _, o := range l.Rooms {
				if o.ID != r.ID && c >= o.X && c < o.X+o.W && r.Y-1 >= o.Y && r.Y-1 < o.Y+o.H {
					covered++
					break
				}
			}
		}
		if covered > 0 && covered < r.W {
			return true
		}
	}
	return false
}
