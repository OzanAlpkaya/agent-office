package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Profile is the user's own, daemon-wide (Phase 18 review, ADR 0058): the
// name on their nameplate and their character's look on every project's
// floor. Empty until saved; the UI then shows "You" and a default look.
type Profile struct {
	Name string      `json:"name"`
	Look ProfileLook `json:"look"`
	Set  bool        `json:"set"` // saved at least once
}

// ProfileLook indexes the office's art (web/src/office/avatar.ts): skin
// tone, hair colour, hair style, shirt and trousers. The UI wraps an index
// beyond its lists, so the daemon only bounds them.
type ProfileLook struct {
	Skin  int `json:"skin"`
	Hair  int `json:"hair"`
	Style int `json:"style"`
	Shirt int `json:"shirt"`
	Pants int `json:"pants"`
}

const (
	maxProfileName = 24
	maxLookIndex   = 63
)

var profileNameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} '.-]*$`)

// Validate checks a profile the user saves.
func (p Profile) Validate() error {
	if p.Name != "" && (utf8.RuneCountInString(p.Name) > maxProfileName || !profileNameRe.MatchString(p.Name)) {
		return invalid("name: at most %d letters, digits, spaces and ' . -", maxProfileName)
	}
	for _, v := range []int{p.Look.Skin, p.Look.Hair, p.Look.Style, p.Look.Shirt, p.Look.Pants} {
		if v < 0 || v > maxLookIndex {
			return invalid("look: every part is a number from 0 to %d", maxLookIndex)
		}
	}
	return nil
}

// Profile returns the user's profile; an unsaved one is empty.
func (h *Hub) Profile(ctx context.Context) (Profile, error) {
	row, err := h.q.GetProfile(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, nil
	}
	if err != nil {
		return Profile{}, err
	}
	p := Profile{Name: row.Name, Set: true}
	if err := json.Unmarshal([]byte(row.Look), &p.Look); err != nil {
		h.log.Warn("profile: unreadable look, using the default", "err", err)
		p.Look = ProfileLook{}
	}
	return p, nil
}

// SetProfile saves the user's profile and tells every open page.
func (h *Hub) SetProfile(ctx context.Context, p Profile) (Profile, error) {
	p.Name = strings.TrimSpace(p.Name)
	if err := p.Validate(); err != nil {
		return Profile{}, err
	}
	look, err := json.Marshal(p.Look)
	if err != nil {
		return Profile{}, err
	}
	if _, err := h.q.SetProfile(ctx, db.SetProfileParams{Name: p.Name, Look: string(look), UpdatedAt: now()}); err != nil {
		return Profile{}, err
	}
	p.Set = true
	h.log.Info("profile saved", "name_set", p.Name != "")
	h.publish(HubUpdate{Type: "profile", Profile: &p})
	return p, nil
}
