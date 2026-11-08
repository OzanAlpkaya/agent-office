package icons

import (
	"bytes"
	"flag"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite web/public/icons from Render")

func TestCommittedIcons(t *testing.T) {
	for _, f := range Files {
		var buf bytes.Buffer
		if err := png.Encode(&buf, Render(f.Size, f.Maskable)); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join("..", "public", "icons", f.Name)
		if *update {
			if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (run go test ./web/icons -update)", err)
		}
		if !bytes.Equal(got, buf.Bytes()) {
			t.Errorf("%s differs from Render (run go test ./web/icons -update)", f.Name)
		}
	}
}

func TestArtUsesThePalette(t *testing.T) {
	for y, row := range art {
		if len(row) != 16 {
			t.Fatalf("row %d has %d cells", y, len(row))
		}
		for x := 0; x < 16; x++ {
			if _, ok := palette[row[x]]; !ok {
				t.Fatalf("row %d col %d: %q not in the palette", y, x, row[x])
			}
		}
	}
}

func TestMaskableArtStaysInTheSafeZone(t *testing.T) {
	const size = 512
	img := Render(size, true)
	bg := palette['.']
	r := 0.4 * size // the safe zone: a circle of 80% of the size
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)-size/2+0.5, float64(y)-size/2+0.5
			if dx*dx+dy*dy > r*r && img.NRGBAAt(x, y) != bg {
				t.Fatalf("pixel %d,%d outside the safe zone is art", x, y)
			}
		}
	}
}
