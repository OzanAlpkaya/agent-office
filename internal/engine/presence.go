package engine

import (
	"slices"
	"sync"
)

// Presence is the user's presence (Phase 18, ADR 0058): the paired devices
// that have the UI open in a visible page. The office seats the user's
// character at their desk while it is not empty.
type Presence struct {
	Devices []string `json:"devices"` // device names, distinct and sorted
}

type page struct {
	device  string
	visible bool
}

// pages is the hub's view of the open UI pages: one per hub event stream.
type pages struct {
	mu   sync.Mutex
	byID map[string]page
	last []string
}

// SetPage records an open page of a device and whether it is visible, and
// publishes the presence when it changed.
func (h *Hub) SetPage(id, device string, visible bool) {
	h.pages.mu.Lock()
	if h.pages.byID == nil {
		h.pages.byID = map[string]page{}
	}
	h.pages.byID[id] = page{device: device, visible: visible}
	p, changed := h.pages.compute()
	h.pages.mu.Unlock()
	if changed {
		h.publish(HubUpdate{Type: "presence", Presence: &p})
	}
}

// DropPage forgets a page whose event stream ended.
func (h *Hub) DropPage(id string) {
	h.pages.mu.Lock()
	delete(h.pages.byID, id)
	p, changed := h.pages.compute()
	h.pages.mu.Unlock()
	if changed {
		h.publish(HubUpdate{Type: "presence", Presence: &p})
	}
}

// Presence returns the user's presence now.
func (h *Hub) Presence() Presence {
	h.pages.mu.Lock()
	defer h.pages.mu.Unlock()
	return Presence{Devices: slices.Clone(h.pages.last)}
}

// compute recomputes the visible devices; the caller holds mu.
func (ps *pages) compute() (Presence, bool) {
	var names []string
	for _, p := range ps.byID {
		if p.visible && !slices.Contains(names, p.device) {
			names = append(names, p.device)
		}
	}
	slices.Sort(names)
	if names == nil {
		names = []string{}
	}
	changed := !slices.Equal(names, ps.last)
	ps.last = names
	return Presence{Devices: slices.Clone(names)}, changed
}
