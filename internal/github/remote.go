package github

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Repo is a GitHub repository.
type Repo struct {
	Owner string
	Name  string
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

// GitHub's rules: owners are alphanumeric with single inner hyphens (at most
// 39 characters); repository names use letters, digits, '.', '-' and '_'.
var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9]){0,38}$`)
	namePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

func (r Repo) validate() error {
	if !ownerPattern.MatchString(r.Owner) || !namePattern.MatchString(r.Name) || r.Name == "." || r.Name == ".." {
		return fmt.Errorf("github: invalid repository %q", r.String())
	}
	return nil
}

// ParseRepo parses "owner/name".
func ParseRepo(s string) (Repo, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(s), "/")
	r := Repo{Owner: owner, Name: strings.TrimSuffix(name, ".git")}
	if !ok {
		return Repo{}, fmt.Errorf("github: repository must be owner/name, got %q", s)
	}
	return r, r.validate()
}

// ParseRemote extracts owner/name from a git remote URL (ADR 0020):
// git@<host>:owner/name(.git) (scp-like SSH, the host may be an SSH alias),
// ssh://git@<host>[:port]/owner/name(.git), or https://github.com/owner/name(.git).
func ParseRemote(remote string) (Repo, error) {
	remote = strings.TrimSpace(remote)
	fail := func() (Repo, error) {
		return Repo{}, fmt.Errorf("github: cannot read owner/name from the remote URL %q", remote)
	}
	var path string
	switch {
	case strings.HasPrefix(remote, "ssh://"), strings.HasPrefix(remote, "https://"):
		u, err := url.Parse(remote)
		if err != nil || u.Host == "" {
			return fail()
		}
		if u.Scheme == "https" && (u.Host != "github.com" || u.User != nil) {
			return fail() // https only for github.com itself, without credentials in the URL
		}
		path = strings.TrimPrefix(u.Path, "/")
	case !strings.Contains(remote, "://") && strings.Contains(remote, ":"):
		host, p, _ := strings.Cut(remote, ":")
		if host == "" || strings.Contains(host, "/") {
			return fail()
		}
		path = p
	default:
		return fail()
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	if strings.Count(path, "/") != 1 {
		return fail()
	}
	r, err := ParseRepo(path)
	if err != nil {
		return fail()
	}
	return r, nil
}
