package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/OzanAlpkaya/agent-office/internal/pack"
	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// packCommand runs `agent-office pack list|check` (ADR 0023) and returns the
// exit code: 1 if any listed or checked pack is invalid, 2 for usage errors.
func packCommand(args []string, stdout, stderr io.Writer) int {
	usage := "usage: agent-office pack list [--packs-dir <dir>]\n       agent-office pack check <dir>..."
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("pack list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		def, err := pack.DefaultDir()
		if err != nil {
			fmt.Fprintln(stderr, "agent-office:", err)
			return 1
		}
		dir := fs.String("packs-dir", def, "your stack packs ["+pack.DirEnv+"]")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		entries, err := pack.List(*dir)
		code := 0
		if err != nil {
			fmt.Fprintln(stderr, "agent-office:", err)
			code = 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSOURCE\tIMAGE\tDESCRIPTION")
		var invalid []pack.Entry
		for _, e := range entries {
			if e.Err != nil {
				fmt.Fprintf(tw, "%s\t%s\t-\tINVALID (see below)\n", e.Name, e.Source)
				invalid = append(invalid, e)
				continue
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Name, e.Source, imageOf(e.Pack), e.Pack.Description)
		}
		tw.Flush()
		fmt.Fprintf(stdout, "\nUser packs: %s (one directory per pack; see docs/packs.md)\n", *dir)
		for _, e := range invalid {
			fmt.Fprintf(stdout, "\n%v\n", e.Err)
			code = 1
		}
		return code
	case "check":
		if len(args) < 2 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		code := 0
		for _, dir := range args[1:] {
			p, err := pack.LoadDir(dir)
			if err != nil {
				fmt.Fprintln(stdout, err)
				code = 1
				continue
			}
			fmt.Fprintf(stdout, "%s: ok (pack %s, image %s)\n", dir, p.Name, imageOf(p))
			describe(stdout, p)
		}
		return code
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
}

// imageOf is the worker image a valid pack's agents get.
func imageOf(p *pack.Pack) string {
	files, err := p.BuildFiles()
	switch {
	case err != nil:
		return "error: " + err.Error()
	case files == nil:
		return sandbox.BaseImage() + " (base)"
	}
	return sandbox.PackImage(p.Name, files)
}

func describe(w io.Writer, p *pack.Pack) {
	none := func(s []string) string {
		if len(s) == 0 {
			return "none"
		}
		return strings.Join(s, ", ")
	}
	var env []string
	for _, v := range p.Env {
		n := v.Name
		if v.Required {
			n += " (required)"
		}
		env = append(env, n)
	}
	fmt.Fprintf(w, "  egress:      %s\n  secrets:     %s\n  skills:      %s\n  mcp servers: %s\n",
		none(p.Egress), none(env), none(p.Skills), none(p.ServerNames()))
}
