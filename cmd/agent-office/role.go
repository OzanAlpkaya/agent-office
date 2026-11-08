package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/OzanAlpkaya/agent-office/internal/role"
)

// roleCommand runs `agent-office role list|check` (ADR 0029) and returns the
// exit code: 1 if any listed or checked role is invalid, 2 for usage errors.
func roleCommand(args []string, stdout, stderr io.Writer) int {
	usage := "usage: agent-office role list [--roles-dir <dir>]\n       agent-office role check <dir>..."
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("role list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		def, err := role.DefaultDir()
		if err != nil {
			fmt.Fprintln(stderr, "agent-office:", err)
			return 1
		}
		dir := fs.String("roles-dir", def, "your roles ["+role.DirEnv+"]")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		entries, err := role.List(*dir)
		code := 0
		if err != nil {
			fmt.Fprintln(stderr, "agent-office:", err)
			code = 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tBEHAVIOUR\tWORKSPACE\tMODEL\tSOURCE\tDESCRIPTION")
		var bad []role.Entry
		for _, e := range entries {
			if e.Err != nil {
				fmt.Fprintf(tw, "%s\t-\t-\t-\t%s\tINVALID (see below)\n", e.Name, e.Source)
				bad = append(bad, e)
				continue
			}
			src := e.Source
			if e.Replaces {
				src += " (replaces the built-in)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, e.Role.Behaviour, workspaceOf(e.Role), modelOrDefault(e.Role),
				src, e.Role.Description)
		}
		tw.Flush()
		fmt.Fprintf(stdout, "\nUser roles: %s (one directory per role; see docs/roles.md)\n", *dir)
		for _, e := range bad {
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
			r, err := role.LoadDir(dir)
			if err != nil {
				fmt.Fprintln(stdout, err)
				code = 1
				continue
			}
			fmt.Fprintf(stdout, "%s: ok (role %s, behaviour %s, %s, model %s, reports to %s)\n", dir, r.Name, r.Behaviour,
				workspaceOf(r), modelOrDefault(r), r.ReportsTo)
			fmt.Fprintf(stdout, "  team tools: %s\n", strings.Join(r.TeamTools, ", "))
			if d := r.DenyRules(); len(d) > 0 {
				fmt.Fprintf(stdout, "  denied:     %s\n", strings.Join(d, ", "))
			}
		}
		return code
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
}

func workspaceOf(r *role.Role) string {
	if r.ReadOnly {
		return "read-only"
	}
	return "read-write"
}

func modelOrDefault(r *role.Role) string {
	if r.Model == "" {
		return "(--model)"
	}
	return r.Model
}
