import { describe, expect, it } from "vitest";
import * as art from "./art";
import {
  avatarPalette, defaultLook, displayName, lookKey, lookOfProfile, normalizeLook, outfits, parts, randomLook, sceneLook, trousers,
} from "./avatar";

describe("the user's avatar", () => {
  it("has a part for every choice of the office's art", () => {
    expect(parts.map((p) => [p.key, p.count])).toEqual([
      ["skin", art.skins.length], ["hair", art.hairColors.length], ["style", art.hairStyles.length],
      ["shirt", outfits.length], ["pants", trousers.length],
    ]);
  });

  it("shows a default name and look until a profile is saved", () => {
    expect(displayName(undefined)).toBe("You");
    expect(displayName({ name: "  ", look: defaultLook, set: true })).toBe("You");
    expect(displayName({ name: "Ozan", look: defaultLook, set: true })).toBe("Ozan");
    expect(lookOfProfile(undefined)).toEqual(defaultLook);
    expect(lookOfProfile({ name: "", look: { skin: 3, hair: 0, style: 2, shirt: 1, pants: 0 }, set: false })).toEqual(defaultLook);
    expect(lookOfProfile({ name: "", look: { skin: 3, hair: 0, style: 2, shirt: 1, pants: 0 }, set: true }))
      .toEqual({ skin: 3, hair: 0, style: 2, shirt: 1, pants: 0 });
  });

  it("keeps a saved look within the art, whatever the daemon holds", () => {
    expect(normalizeLook({ skin: art.skins.length + 1, hair: -1, style: 1.5 })).toEqual({
      ...defaultLook, skin: 1, hair: defaultLook.hair, style: defaultLook.style,
    });
    expect(normalizeLook(undefined)).toEqual(defaultLook);
  });

  it("Randomize always gives a new look, within the art", () => {
    let look = defaultLook;
    const seen = new Set<string>();
    for (let i = 0; i < 50; i++) {
      const next = randomLook(look);
      expect(lookKey(next)).not.toBe(lookKey(look));
      for (const p of parts) {
        expect(next[p.key]).toBeGreaterThanOrEqual(0);
        expect(next[p.key]).toBeLessThan(p.count);
      }
      seen.add(lookKey(next));
      look = next;
    }
    expect(seen.size).toBeGreaterThan(30);
    // a random source that always repeats still changes something
    const stuck = { skin: 0, hair: 0, style: 0, shirt: 0, pants: 0 };
    expect(lookKey(randomLook(stuck, () => 0))).not.toBe(lookKey(stuck));
  });

  it("colours the office's person maps, trousers included", () => {
    const l = { skin: 2, hair: 3, style: 4, shirt: 7, pants: 2 };
    const p = avatarPalette(l);
    expect(p.s).toBe(art.skins[2].base);
    expect(p.h).toBe(art.hairColors[3].base);
    expect(p.c).toBe(outfits[7].base);
    expect(p.n).toBe(trousers[2]);
    expect(sceneLook(l)).toEqual({ skin: 2, hair: 3, style: art.hairStyles[4], phase: 0 });
  });
});
