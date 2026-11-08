package usage

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"
)

//go:embed prices.yaml
var builtinPrices []byte

// PricesFileEnv names the user's price file (ADR 0026).
const PricesFileEnv = "AGENT_OFFICE_PRICES_FILE"

const maxPricesFile = 64 << 10

// Money is integer, never a float (ADR 0026): prices are nano-USD per token
// (USD per million tokens × 1000, exact for every published price), costs are
// nano-USD, and reports round to micro-USD.

// Rates are nano-USD per token.
type Rates struct {
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	CacheRead    int64 `json:"cache_read"`
	CacheWrite5m int64 `json:"cache_write_5m"`
	CacheWrite1h int64 `json:"cache_write_1h"`
}

// Price is one model's entry.
type Price struct {
	Rates
	Long  *LongContext `json:"long_context,omitempty"` // other rates above a prompt length
	Fast  int64        `json:"fast,omitempty"`         // multiplier for fast mode, in permille; 0: none
	GeoUS int64        `json:"geo_us,omitempty"`       // multiplier for US-only inference, in permille; 0: none
}

// MicroUSD rounds nano-USD to micro-USD (half up).
func MicroUSD(nano int64) int64 { return (nano + 500) / 1000 }

// fixed converts a decimal from the YAML file to thousandths, refusing values
// with more than three decimal places.
func fixed(v float64) (int64, bool) {
	n := math.Round(v * 1000)
	return int64(n), math.Abs(v*1000-n) < 1e-6
}

// LongContext prices prompts longer than Above tokens.
type LongContext struct {
	Above int64 `json:"above"`
	Rates
}

// Prices is a price table.
type Prices struct {
	AsOf   string           `json:"as_of"`
	Source string           `json:"source"`
	File   string           `json:"file,omitempty"` // the user file merged in, if any
	Models map[string]Price `json:"models"`
}

// Builtin returns the table embedded in the binary.
func Builtin() *Prices {
	p, err := parsePrices(builtinPrices, "built-in prices.yaml", true)
	if err != nil {
		panic(err) // tested; a broken embedded table is a build bug
	}
	return p
}

// DefaultPricesFile is $XDG_CONFIG_HOME/agent-office/prices.yaml, or the
// environment's override.
func DefaultPricesFile() (string, error) {
	if p := os.Getenv(PricesFileEnv); p != "" {
		return filepath.Abs(p)
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "agent-office", "prices.yaml"), nil
}

// Load returns the built-in table with the user file at path merged in: its
// models replace or add entries. A missing file is not an error unless
// required (the user named it explicitly).
func Load(path string, required bool) (*Prices, error) {
	p := Builtin()
	if path == "" {
		return p, nil
	}
	b, err := readOwned(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	user, err := parsePrices(b, path, false)
	if err != nil {
		return nil, err
	}
	for k, v := range user.Models {
		p.Models[k] = v
	}
	if user.AsOf != "" {
		p.AsOf = user.AsOf
	}
	if user.Source != "" {
		p.Source = user.Source
	}
	p.File = path
	return p, nil
}

// readOwned reads a user-owned config file: a regular file (no symlink) of
// the current user, not writable by group or others, in a directory with the
// same properties.
func readOwned(path string) ([]byte, error) {
	uid := uint32(os.Getuid())
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if st, ok := dir.Sys().(*syscall.Stat_t); !dir.IsDir() || !ok || st.Uid != uid || dir.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s: its directory must be owned by you and not writable by others (chmod go-w %s)", path, filepath.Dir(path))
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s: must be a regular file, not a symlink", path)
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !fi.Mode().IsRegular() || !ok || st.Uid != uid || fi.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s: must be a regular file owned by you and not writable by others (chmod go-w %s)", path, path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxPricesFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxPricesFile {
		return nil, fmt.Errorf("%s: larger than %d bytes", path, maxPricesFile)
	}
	return b, nil
}

// The YAML shapes. Pointers tell a missing rate from a zero one.
type yamlRates struct {
	Input        *float64 `yaml:"input"`
	Output       *float64 `yaml:"output"`
	CacheRead    *float64 `yaml:"cache_read"`
	CacheWrite5m *float64 `yaml:"cache_write_5m"`
	CacheWrite1h *float64 `yaml:"cache_write_1h"`
}

type yamlPrice struct {
	yamlRates `yaml:",inline"`
	Long      *struct {
		Above     *int64 `yaml:"above"`
		yamlRates `yaml:",inline"`
	} `yaml:"long_context"`
	Fast  *float64 `yaml:"fast"`
	GeoUS *float64 `yaml:"geo_us"`
}

type yamlPrices struct {
	AsOf   string               `yaml:"as_of"`
	Source string               `yaml:"source"`
	Models map[string]yamlPrice `yaml:"models"`
}

var modelRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{0,99}$`)

// PricesError lists every problem of a price file, each with its fix.
type PricesError struct {
	File     string
	Problems []string
}

func (e *PricesError) Error() string {
	return e.File + ": " + strings.Join(e.Problems, "; ")
}

func parsePrices(b []byte, file string, builtin bool) (*Prices, error) {
	perr := &PricesError{File: file}
	add := func(format string, a ...any) { perr.Problems = append(perr.Problems, fmt.Sprintf(format, a...)) }
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var y yamlPrices
	if err := dec.Decode(&y); err != nil {
		if errors.Is(err, io.EOF) {
			add("the file is empty; add a models: map")
			return nil, perr
		}
		add("%s; fix the YAML (fields: as_of, source, models)", oneLine(err.Error()))
		return nil, perr
	}
	var extra yamlPrices
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		add("more than one YAML document; keep one")
	}
	if y.AsOf != "" {
		if _, err := time.Parse(time.DateOnly, y.AsOf); err != nil {
			add("as_of: %q is not a date; use YYYY-MM-DD", y.AsOf)
		}
	} else if builtin {
		add("as_of: missing")
	}
	if len(y.Source) > 300 {
		add("source: longer than 300 characters")
	}
	if len(y.Models) == 0 {
		add("models: empty; add at least one model")
	}
	p := &Prices{AsOf: y.AsOf, Source: y.Source, Models: map[string]Price{}}
	names := make([]string, 0, len(y.Models))
	for k := range y.Models {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		yp := y.Models[name]
		field := "models." + name
		if !modelRe.MatchString(name) {
			add("%s: not a model id; use the API's id, e.g. claude-haiku-5-5", field)
			continue
		}
		price := Price{Rates: rates(yp.yamlRates, field, add)}
		if yp.Long != nil {
			if yp.Long.Above == nil || *yp.Long.Above <= 0 {
				add("%s.long_context.above: missing or not positive; the prompt length (tokens) above which these rates apply", field)
			} else {
				price.Long = &LongContext{Above: *yp.Long.Above, Rates: rates(yp.Long.yamlRates, field+".long_context", add)}
			}
		}
		for _, m := range []struct {
			v    *float64
			name string
			dst  *int64
		}{{yp.Fast, "fast", &price.Fast}, {yp.GeoUS, "geo_us", &price.GeoUS}} {
			if m.v == nil {
				continue
			}
			pm, ok := fixed(*m.v)
			if !finite(*m.v) || *m.v < 1 || *m.v > 100 || !ok {
				add("%s.%s: %v is not a multiplier between 1 and 100 with at most 3 decimals", field, m.name, *m.v)
				continue
			}
			*m.dst = pm
		}
		p.Models[name] = price
	}
	if len(perr.Problems) > 0 {
		return nil, perr
	}
	return p, nil
}

func rates(y yamlRates, field string, add func(string, ...any)) Rates {
	var r Rates
	for _, f := range []struct {
		v    *float64
		name string
		dst  *int64
	}{{y.Input, "input", &r.Input}, {y.Output, "output", &r.Output}, {y.CacheRead, "cache_read", &r.CacheRead},
		{y.CacheWrite5m, "cache_write_5m", &r.CacheWrite5m}, {y.CacheWrite1h, "cache_write_1h", &r.CacheWrite1h}} {
		switch {
		case f.v == nil:
			add("%s.%s: missing; USD per million tokens", field, f.name)
		case !finite(*f.v) || *f.v > 10000:
			add("%s.%s: %v is not a price between 0 and 10000 USD per million tokens", field, f.name, *f.v)
		default:
			n, ok := fixed(*f.v)
			if !ok {
				add("%s.%s: %v has more than 3 decimal places (USD per million tokens)", field, f.name, *f.v)
				break
			}
			*f.dst = n
		}
	}
	return r
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

var dateSuffix = regexp.MustCompile(`-\d{8}$`)

// Lookup finds the price of a model id; an id with a date suffix
// (claude-haiku-4-5-20251001) matches its base id.
func (p *Prices) Lookup(model string) (Price, bool) {
	if pr, ok := p.Models[model]; ok {
		return pr, true
	}
	if base := dateSuffix.ReplaceAllString(model, ""); base != model {
		pr, ok := p.Models[base]
		return pr, ok
	}
	return Price{}, false
}

// Cost is the estimated cost of one message in nano-USD; false when its model
// has no price. The arithmetic saturates instead of overflowing.
func (p *Prices) Cost(m Message) (int64, bool) {
	pr, ok := p.Lookup(m.Model)
	if !ok {
		return 0, false
	}
	r := pr.Rates
	if pr.Long != nil && m.Prompt() > pr.Long.Above {
		r = pr.Long.Rates
	}
	var nano int64
	for _, x := range [][2]int64{{m.Input, r.Input}, {m.Output, r.Output}, {m.CacheRead, r.CacheRead},
		{m.CacheWrite5m, r.CacheWrite5m}, {m.CacheWrite1h, r.CacheWrite1h}} {
		nano = AddNano(nano, mulNano(x[0], x[1]))
	}
	if m.Speed == "fast" && pr.Fast > 0 {
		nano = mulNano(nano, pr.Fast) / 1000
	}
	if m.Geo == "us" && pr.GeoUS > 0 {
		nano = mulNano(nano, pr.GeoUS) / 1000
	}
	return nano, true
}

// AddNano adds two non-negative amounts, saturating at the largest int64:
// absurd token counts (a corrupt or hostile transcript) give a huge cost,
// never a wrapped negative one.
func AddNano(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func mulNano(a, b int64) int64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > math.MaxInt64/b {
		return math.MaxInt64
	}
	return a * b
}
