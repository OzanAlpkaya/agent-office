# 0058. The user at their desk: presence from the open, visible UI pages

- **Status:** Accepted
- **Date:** 2026-11-12
- **Amends:** ADR 0045 (the user's office)

## Context

The user's office (ADR 0045) has a desk nobody sits at. Ozan asked that his own character sit
there while a device has the UI open, and the desk be empty when none does.

## Options considered

1. **Count the open WebSockets.** Pros: no client change. Cons: a phone with the PWA in the
   background keeps its socket for a while; a hidden tab counts as present.
2. **The page reports its visibility** on the hub's event stream it already holds (one per page),
   on connect and on `visibilitychange`; the hub keeps the pages with their paired device's name.
3. **A heartbeat endpoint.** Cons: polling, and a delay before "away".

## Decision

Option 2. A page sends `{"type":"presence","visible":bool}`; the hub records it per page
(`Hub.SetPage`, `DropPage` when the stream ends) and publishes `presence` (the distinct names of
devices with a visible page, sorted) when that list changes, and sends the current one after the
project snapshot. Messages are bounded (1 KB) and anything else from the page is ignored. A page
that never reports counts for nothing.

The office seats the user's character at the user's desk while the list is not empty (its own look
and shirt, a "You" nameplate whose tooltip names the devices) and shows an empty desk with the
screen off otherwise. The character is drawn still: the user is present whenever the office is
looked at, and an office where no agent moves must not redraw (ADR 0024).

## Consequences

- From the viewer's own page the user is nearly always at their desk; the empty desk shows on a
  page that is hidden and another device's view, and is the honest answer to "is anyone watching".
- Presence carries device names only, to paired devices (who see them under Settings → Devices).
- Agents are not told whether the user is present.

## Amendment (2026-11-12, Phase 18 review): the user's name and look

Settings → Profile has a name for the user's nameplate (empty: "You") and an avatar creator: a live
preview drawn with the office's own pixel maps and palettes (the arms and the pixel drawing moved to
pure modules the scene and the preview share), Randomize for a new look on each click (skin tone,
hair colour, hair style, shirt, trousers), and Save. The profile is daemon-wide (migration 0018,
`GET/PUT /api/profile`, validated), sent on the hub stream after the presence and when it changes, and
used on every project's floor. The look is a set of indexes into the art's lists; the daemon only
bounds them and the UI wraps them, so the art can grow. Unsaved, the user has a default look of their
own (agents' looks come from hashes of their ids).

