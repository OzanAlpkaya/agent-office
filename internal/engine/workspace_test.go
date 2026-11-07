package engine

import (
	"strings"
	"testing"

	"github.com/OzanAlpkaya/agent-office/internal/git"
)

func TestCheckCommits(t *testing.T) {
	co := "agent-abc123 (agent-office) <noreply@agent-office.invalid>"
	good := git.Commit{Hash: "1111", AuthorName: "Ozan", AuthorEmail: "o@example.com",
		CommitterName: "Ozan", CommitterEmail: "o@example.com", CoAuthors: []string{"someone <s@x>", co}}
	noTrailer := good
	noTrailer.Hash, noTrailer.CoAuthors = "2222", nil
	otherAuthor := good
	otherAuthor.Hash, otherAuthor.AuthorName, otherAuthor.CommitterEmail = "3333", "Mallory", "m@x"
	flagged := checkCommits([]git.Commit{good, noTrailer, otherAuthor}, "Ozan", "o@example.com", co)
	if len(flagged) != 2 || flagged[0].Commit != "2222" || flagged[1].Commit != "3333" {
		t.Fatalf("flagged %+v", flagged)
	}
	if !strings.Contains(flagged[0].Problem, "no Co-Authored-By") ||
		!strings.Contains(flagged[1].Problem, "author is Mallory") || !strings.Contains(flagged[1].Problem, "committer is") {
		t.Fatalf("problems %+v", flagged)
	}
	if f := checkCommits(nil, "Ozan", "o@example.com", co); f == nil || len(f) != 0 {
		t.Fatal("no commits must give an empty, non-nil list")
	}
}
