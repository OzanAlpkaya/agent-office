// The user's character (Phase 18 review, ADR 0058): a name and a look made
// of the office's own art: skin tone, hair colour, hair style, shirt and
// trousers. Saved daemon-wide (GET/PUT /api/profile). Pure.
import * as art from "./art";
import type { Look } from "./pose";

export interface AvatarLook {
  skin: number;
  hair: number;
  style: number;
  shirt: number;
  pants: number;
}

export interface Profile {
  name: string;
  look: AvatarLook;
  set: boolean; // saved at least once
}

// The outfits: every shirt the office has, kinds' and role packs'.
export const outfits: readonly art.Swatch[] = [...Object.values(art.shirts), ...art.roleShirts];
export const trousers: readonly number[] = [0x4a5060, 0x2e3a5a, 0x5a4632, 0x34343c, 0x6a6e78, 0x3d5a46];

// The parts and how many choices each has, for the creator and Randomize.
export const parts: readonly { key: keyof AvatarLook; label: string; count: number }[] = [
  { key: "skin", label: "Skin tone", count: art.skins.length },
  { key: "hair", label: "Hair colour", count: art.hairColors.length },
  { key: "style", label: "Hair style", count: art.hairStyles.length },
  { key: "shirt", label: "Shirt", count: outfits.length },
  { key: "pants", label: "Trousers", count: trousers.length },
];

// The default: a look of its own, not an agent's (they come from hashes of ids).
export const defaultLook: AvatarLook = { skin: 1, hair: 1, style: 0, shirt: outfits.length - 3, pants: 1 };
export const defaultName = "You";

const wrap = (v: unknown, n: number, d: number): number =>
  typeof v === "number" && Number.isInteger(v) && v >= 0 ? v % n : d;

// normalizeLook keeps a look within the art's lists, whatever the daemon holds.
export function normalizeLook(raw: Partial<AvatarLook> | undefined): AvatarLook {
  const out = { ...defaultLook };
  for (const p of parts) out[p.key] = wrap(raw?.[p.key], p.count, defaultLook[p.key]);
  return out;
}

export function isProfile(v: unknown): v is Profile {
  if (typeof v !== "object" || v === null) return false;
  const p = v as Record<string, unknown>;
  return typeof p.name === "string" && typeof p.set === "boolean" && typeof p.look === "object" && p.look !== null;
}

// displayName is the name on the user's nameplate.
export const displayName = (p: Profile | undefined): string => p?.name.trim() || defaultName;

// lookOfProfile is the profile's look, or the default when nothing is saved.
export const lookOfProfile = (p: Profile | undefined): AvatarLook => (p?.set ? normalizeLook(p.look) : defaultLook);

// randomLook picks a new look; it differs from current in at least one part.
export function randomLook(current: AvatarLook, rand: () => number = Math.random): AvatarLook {
  for (let tries = 0; tries < 20; tries++) {
    const next = { ...current };
    for (const p of parts) next[p.key] = Math.floor(rand() * p.count) % p.count;
    if (parts.some((p) => next[p.key] !== current[p.key])) return next;
  }
  // a stuck random source: change the shirt
  return { ...current, shirt: (current.shirt + 1) % outfits.length };
}

// avatarPalette colours the office's person maps for a look.
export function avatarPalette(l: AvatarLook): art.Palette {
  return { ...art.personPalette(art.skins[l.skin], art.hairColors[l.hair], outfits[l.shirt]), n: trousers[l.pants] };
}

// sceneLook is the look in the scene's terms (hair style by name).
export function sceneLook(l: AvatarLook): Look {
  return { skin: l.skin, hair: l.hair, style: art.hairStyles[l.style], phase: 0 };
}

// lookKey names a look's textures.
export const lookKey = (l: AvatarLook): string => `you-${l.skin}-${l.hair}-${l.style}-${l.shirt}-${l.pants}`;
