import type { Kind } from "../api";
// Pixel art for the office view (ADR 0024). Every sprite here is original to
// this repository: a pixel map is a list of rows, one character per pixel,
// looked up in a palette; "." is transparent. Some larger pieces (desk, floor,
// wall, window, whiteboard, door, screens) are built by small functions from
// the same alphabet. See ART.md before adding sprites.

export type Color = number; // 0xRRGGBB

export interface PixelMap {
  readonly w: number;
  readonly h: number;
  readonly rows: readonly string[];
}

export type Palette = Readonly<Record<string, Color>>;

export const transparent = ".";

export function pixelMap(rows: readonly string[]): PixelMap {
  return { w: rows[0]?.length ?? 0, h: rows.length, rows };
}

// Fixed colours. Per-agent colours (skin s/S, hair h/H, shirt c/C) come from
// looks below and are merged over these.
export const basePalette: Palette = {
  k: 0x1a1c24, // outline
  e: 0x1a1c24, // eyes
  m: 0xa0524f, // mouth
  w: 0xf2efe6, // white / paper
  b: 0xf7f5ee, // bubble
  g: 0xb9bfcc, // light grey
  G: 0x7d8496, // mid grey
  n: 0x4a5060, // dark grey
  d: 0xb07c48, // desk top
  D: 0x8a5a33, // desk front
  E: 0x9c6a3d, // desk top shade
  q: 0x5e3b20, // desk edge
  a: 0x8a4452, // chair
  A: 0x6a3040, // chair seams
  f: 0x2a2e38, // monitor frame
  F: 0x3c4250, // monitor frame light
  x: 0x0e131c, // screen off
  p: 0x5cb860, // leaf
  P: 0x2f7a3a, // leaf shade
  o: 0xc86f3e, // pot
  O: 0x8e4a26, // pot shade
  l: 0xbfe3ff, // glass / lens / water
  L: 0x8cc4f0, // glass shade
  u: 0xffb454, // amber (alert)
  r: 0xe05a5a, // red
  v: 0x5a8de0, // blue
  y: 0xe8dcc0, // mug
  z: 0x1c2747, // night sky
  Z: 0x24335c, // night sky, lower
  t: 0xfff1b0, // star / moon
};

// Screen colours: background, text and highlights on the monitors.
export const screenPalette: Palette = {
  ".": 0x0e131c,
  "0": 0x111a2a, // screen background
  "1": 0x7aa2f7, // keyword
  "2": 0xc0caf5, // identifier
  "3": 0x9ece6a, // string / added
  "4": 0xe0af68, // number / amber
  "5": 0xf7768e, // removed
  "6": 0x3b4a6b, // dim line / selection
  "7": 0x2ac3de, // cyan
  "8": 0xffb454, // dialog
  "9": 0x1a1c24, // dialog text
};

// ---------------------------------------------------------------- people

// Head and upper body, facing the viewer. The desk hides everything below
// row 15; arms are drawn by the scene so they can move per frame.
export const body = pixelMap([
  "................",
  "................",
  "....kkkkkkkk....",
  "...kssssssssk...",
  "...kssssssssk...",
  "...kssssssssk...",
  "...ksessssesk...",
  "...ksessssesk...",
  "...kssssssssk...",
  "...ksssmmsssk...",
  "....kSSSSSSk....",
  "..kkccSSSScckk..",
  ".kcccccccccccck.",
  ".kcCccccccccCck.",
  "kcCccccccccccCck",
  "kcCccccccccccCck",
]);

// The same with closed eyes, shown for a moment now and then.
export const bodyBlink = pixelMap([
  ...body.rows.slice(0, 6),
  "...kssssssssk...",
  "...kseesseesk...",
  ...body.rows.slice(8),
]);

export type HairStyle = "short" | "long" | "bun" | "part" | "buzz";
export const hairStyles: readonly HairStyle[] = ["short", "long", "bun", "part", "buzz"];

// Hair is drawn over the body.
export const hair: Readonly<Record<HairStyle, PixelMap>> = {
  short: pixelMap([
    "................",
    ".....kkkkkk.....",
    "...kkhhhhhhkk...",
    "..khhhhHhhhhhk..",
    "..khhhhhhhhhhk..",
    "..khhh....hhhk..",
  ]),
  long: pixelMap([
    "................",
    ".....kkkkkk.....",
    "...kkhhhhhhkk...",
    "..khhhhHhhhhhk..",
    "..khhhhhhhhhhk..",
    "..khhhh..hhhhk..",
    "..khh......hhk..",
    "..khh......hhk..",
    "..khh......hhk..",
    "..khh......hhk..",
    "..kkh......hkk..",
    "...kk......kk...",
  ]),
  bun: pixelMap([
    "......kkkk......",
    ".....khhhhk.....",
    "....kkhhHhkk....",
    "...khhhhhhhhk...",
    "...khhhhhhhhk...",
    "...kh......hk...",
  ]),
  part: pixelMap([
    "................",
    "....kkkkkkk.....",
    "...khhhhhhhkk...",
    "..khhhHhhhhhhk..",
    "..khhhhhhhhhhk..",
    "..kh..hhhhhhhk..",
    "..kh.......hhk..",
  ]),
  buzz: pixelMap([
    "................",
    "................",
    "....kkkkkkkk....",
    "...khhhhhhhhk...",
    "...kHhhhhhhHk...",
  ]),
};

export interface Swatch {
  readonly base: Color;
  readonly shade: Color;
}

export const skins: readonly Swatch[] = [
  { base: 0xf6d3b3, shade: 0xe0ac7e },
  { base: 0xe8b98f, shade: 0xc98e62 },
  { base: 0xc68a5a, shade: 0xa06a40 },
  { base: 0x9a6440, shade: 0x7a4a2c },
  { base: 0x6e4529, shade: 0x55331d },
];

export const hairColors: readonly Swatch[] = [
  { base: 0x2a2523, shade: 0x45403c }, // black
  { base: 0x5a3825, shade: 0x7a4e34 }, // brown
  { base: 0xd9b25c, shade: 0xf0d080 }, // blond
  { base: 0xa8462c, shade: 0xc8603e }, // red
  { base: 0x9a9aa6, shade: 0xc0c0ca }, // grey
  { base: 0x4b6fd1, shade: 0x6d8ee6 }, // dyed blue
];

// Shirt colours by kind, matching the role colours of the rest of the UI.
export const shirts: Readonly<Record<Kind, Swatch>> = {
  architect: { base: 0xd99a3f, shade: 0xb07a2a },
  lead: { base: 0x9b6dde, shade: 0x7a4fbf },
  worker: { base: 0x4f86d9, shade: 0x3a68b0 },
  qa: { base: 0xd9605a, shade: 0xb04640 },
  reviewer: { base: 0x3fb8ab, shade: 0x2d8f85 },
  assistant: { base: 0x8fbf5a, shade: 0x6f9a40 },
};

export function personPalette(skin: Swatch, hairColor: Swatch, shirt: Swatch): Palette {
  return {
    ...basePalette,
    s: skin.base, S: skin.shade,
    h: hairColor.base, H: hairColor.shade,
    c: shirt.base, C: shirt.shade,
  };
}

// ---------------------------------------------------------------- furniture

export const chair = pixelMap([
  "....kkkkkkkk....",
  "...kaaaaaaaak...",
  "..kaaAaaaaAaak..",
  "..kaAaaaaaaAak..",
  "..kaAaaaaaaAak..",
  "..kaAaaaaaaAak..",
  "..kaaAaaaaAaak..",
  "...kaaaaaaaak...",
  "....kkkkkkkk....",
  ".......kk.......",
  "..kkkkkkkkkkkk..",
  ".kaaaaaaaaaaaak.",
  ".kkkkkkkkkkkkkk.",
]);

export const monitor = pixelMap([
  ".kkkkkkkkkkkkkk.",
  "kffffffffffffffk",
  "kfxxxxxxxxxxxxfk",
  "kfxxxxxxxxxxxxfk",
  "kfxxxxxxxxxxxxfk",
  "kfxxxxxxxxxxxxfk",
  "kfxxxxxxxxxxxxfk",
  "kfxxxxxxxxxxxxfk",
  "kfxxxxxxxxxxxxfk",
  "kfxxxxxxxxxxxxfk",
  "kffffffFFffffffk",
  ".kkkkkkFFkkkkkk.",
  "......kFFk......",
  "....kkFFFFkk....",
]);

// Where the screen sits inside the monitor, and its size.
export const screenRect = { x: 2, y: 2, w: 12, h: 8 } as const;

export const keyboard = pixelMap([
  "kkkkkkkkkkkk",
  "kgngngngngnk",
  "kkkkkkkkkkkk",
]);

export const mug = pixelMap([
  "kkkk..",
  "kyykkk",
  "kyyk.k",
  "kyykkk",
  "kkkk..",
]);

export const magnifier = pixelMap([
  ".kkk...",
  "kllLk..",
  "klllk..",
  "kLllk..",
  ".kkkn..",
  "....nn.",
  ".....nn",
]);

export const envelope = pixelMap([
  "kkkkkkkk",
  "kkwwwwkk",
  "kwkwwkwk",
  "kwwkkwwk",
  "kwwwwwwk",
  "kkkkkkkk",
]);

export const plant = pixelMap([
  ".....pp.....",
  "...p.pp.p...",
  "..pPp..pPp..",
  ".pPPpppPPp..",
  ".pPpPppPpPp.",
  "..ppPPPPpp..",
  ".pPppPPppPp.",
  ".pPPpPPpPPp.",
  "..ppPppPpp..",
  "...pPPPPp...",
  "....pPPp....",
  "..kkkkkkkk..",
  "..kooooOOk..",
  "...kooOOk...",
  "...kooOOk...",
  "...kooOOk...",
  "....kkkk....",
]);

export const waterCooler = pixelMap([
  "..kkkkkk..",
  ".kllllllk.",
  ".kllLLllk.",
  ".kllllllk.",
  ".kllllllk.",
  "..kkkkkk..",
  "..kggggk..",
  ".kggggggk.",
  ".kggrvggk.",
  ".kggggggk.",
  ".kGGGGGGk.",
  ".kGGGGGGk.",
  ".kGGGGGGk.",
  ".kGGGGGGk.",
  ".kGGGGGGk.",
  ".kGGGGGGk.",
  ".kGGGGGGk.",
  ".kGGGGGGk.",
  ".kkkkkkkk.",
]);

// ---------------------------------------------------------------- bubbles

const bubbleFrame = (inner: readonly string[], fill: string): PixelMap =>
  pixelMap([
    ".kkkkkkk.",
    ...inner.map((r) => `k${r}k`),
    `.kk${fill}kkkk.`,
    "..kk.....",
  ]);

// "…" thinking dots, three frames.
export const bubbleDots: readonly PixelMap[] = [1, 2, 3].map((n) =>
  bubbleFrame(
    [
      "bbbbbbb",
      "bbbbbbb",
      Array.from({ length: 7 }, (_, x) => (x % 2 === 1 && (x + 1) / 2 <= n ? "k" : "b")).join(""),
      "bbbbbbb",
      "bbbbbbb",
    ],
    "b",
  ),
);

export const bubbleAlert = bubbleFrame(
  [
    "uuukuuu",
    "uuukuuu",
    "uuukuuu",
    "uuuuuuu",
    "uuukuuu",
  ],
  "u",
);

// ---------------------------------------------------------------- built pieces

function grid(w: number, h: number, at: (x: number, y: number) => string): PixelMap {
  return pixelMap(Array.from({ length: h }, (_, y) => Array.from({ length: w }, (_, x) => at(x, y)).join("")));
}

// Small deterministic noise so built pieces look hand-placed but never change.
function noise(x: number, y: number, seed: number): number {
  let n = (x * 374761393 + y * 668265263 + seed * 2147483647) | 0;
  n = Math.imul(n ^ (n >>> 13), 1274126177);
  return ((n ^ (n >>> 16)) >>> 0) / 0xffffffff;
}

// Desk seen from the front and slightly above: a top surface and a front
// panel with a drawer.
export const desk = grid(44, 18, (x, y) => {
  const edgeX = x === 0 || x === 43;
  if (y === 0) return x === 0 || x === 43 ? "." : "q";
  if (y <= 5) {
    if (edgeX) return "q";
    return y === 5 ? "E" : "d";
  }
  if (y === 6) return "q";
  if (y === 17) return x < 4 || x > 39 ? "q" : "."; // legs
  if (edgeX) return "q";
  // drawer on the right, legs at both ends
  if (x >= 28 && x <= 40 && (y === 8 || y === 13)) return "q";
  if ((x === 28 || x === 40) && y > 8 && y < 13) return "q";
  if (x >= 32 && x <= 36 && y === 10) return "G";
  return "D";
});

// Wooden planks, 5 pixels wide, with a joint every 48 pixels at a different
// place on each plank.
export const floorTile = grid(48, 20, (x, y) => {
  const plank = Math.floor(y / 5);
  if (y % 5 === 4) return "2";
  if (x === (plank * 19 + 7) % 48) return "2";
  const n = noise(x >> 2, y, plank);
  return n < 0.18 ? "1" : n > 0.9 ? "3" : "0";
});

export const floorPalette: Palette = { "0": 0x58432f, "1": 0x5f4934, "2": 0x45342a, "3": 0x523e2c };

// One column of wall: plain wall, a chair rail, and a baseboard.
export const wallHeight = 44;
export const wallTile = grid(16, wallHeight, (x, y) => {
  if (y >= wallHeight - 4) return y === wallHeight - 4 ? "3" : "2";
  if (y === 28) return "3";
  if (y > 28) return x % 8 === 0 ? "1" : "4";
  return noise(x, y, 7) < 0.05 ? "1" : "0";
});

export const wallPalette: Palette = {
  "0": 0x2b3148, "1": 0x262b40, "2": 0x1c2033, "3": 0x3b4363, "4": 0x30374f,
};

export const windowMap = grid(32, 22, (x, y) => {
  if (x === 0 || y === 0 || x === 31 || y === 21) return "k";
  if (x === 1 || y === 1 || x === 30 || y === 20 || x === 15 || x === 16 || y === 10) return "g";
  // moon in the left pane
  const mx = x - 9, my = y - 6;
  if (mx * mx + my * my <= 5 && !((mx + 1) * (mx + 1) + my * my <= 4)) return "t";
  if (noise(x, y, 3) < 0.035) return "t";
  return y > 14 ? "Z" : "z";
});

export const whiteboardSize = { w: 76, h: 32 } as const;
export const whiteboardMap = grid(whiteboardSize.w, whiteboardSize.h, (x, y) => {
  const { w, h } = whiteboardSize;
  if (y >= h - 3) {
    // tray with two markers
    if (y === h - 3) return x < 4 || x > w - 5 ? "." : "k";
    if (y === h - 2) {
      if (x < 4 || x > w - 5) return ".";
      if (x >= 8 && x <= 12) return "r";
      if (x >= 15 && x <= 19) return "v";
      return x === 4 || x === w - 5 ? "k" : "G";
    }
    return x < 4 || x > w - 5 ? "." : "k";
  }
  if (x === 0 || y === 0 || x === w - 1 || y === h - 4) return "k";
  if (x === 1 || y === 1 || x === w - 2 || y === h - 5) return "g";
  return "w";
});

// Where sticky notes go on the whiteboard: a grid inside its frame.
export const noteGrid = { x: 4, y: 9, cols: 10, rows: 2, dx: 7, dy: 8 } as const;
// The goal's "title", scribbled above the notes.
export const titleLine = { x: 4, y: 4, w: 40 } as const;

// A sticky note: "U" is the darker glued strip, "u" the note. The scene
// recolours both per task status.
export const noteMap = pixelMap([
  "UUUUUU",
  "uuuuuu",
  "uuuuuu",
  "uuuuuu",
  "uuuuuu",
]);

export const door = grid(22, 34, (x, y) => {
  if (x === 0 || x === 21 || y === 0) return "k";
  if (x === 1 || x === 20 || y === 1) return "g";
  if (x === 2 || x === 19 || y === 2) return "k";
  if (x === 16 && y >= 16 && y <= 18) return "u"; // handle
  if (x === 10 || x === 11) return y > 2 ? "q" : "D";
  if ((y === 8 || y === 24) && x > 3 && x < 18 && x !== 10 && x !== 11) return "q";
  return "D";
});

// A soft rug under the lead's desk.
export const rug = (w: number, h: number): PixelMap =>
  grid(w, h, (x, y) => {
    if (x === 0 || y === 0 || x === w - 1 || y === h - 1) return "2";
    if (x === 2 || y === 2 || x === w - 3 || y === h - 3) return "1";
    return "0";
  });

export const rugPalette: Palette = { "0": 0x3d2f52, "1": 0x8a6fbf, "2": 0x2b2140 };

// ---------------------------------------------------------------- screens

// Screens are drawn in screenPalette. Each returns frames of 12×8.
const W = screenRect.w;
const H = screenRect.h;

export function screenOff(): PixelMap {
  return grid(W, H, () => ".");
}

// A boot screen: a progress bar that fills up.
export function screenBoot(): PixelMap[] {
  return [2, 5, 8, 10].map((fill) =>
    grid(W, H, (x, y) => {
      if (y === 2 && x >= 4 && x <= 7) return "7";
      if (y === 5 && x >= 1 && x <= 10) return x - 1 < fill ? "1" : "6";
      return "0";
    }),
  );
}

// Code that scrolls: one tall map, shown through a moving window. Lines have
// indentation and tokens in a few colours.
export const codeHeight = 48;
export const codeMap = grid(W, codeHeight, (x, y) => {
  if (y % 2 === 1) return "0";
  const line = y / 2;
  const indent = [0, 1, 2, 2, 1, 2, 3, 1, 0, 1, 2, 1][line % 12];
  const len = 3 + Math.floor(noise(line, 1, 11) * 7);
  if (x < indent + 1 || x >= indent + 1 + len || x >= W - 1) return "0";
  const t = x - indent - 1;
  if (t < 2) return line % 3 === 0 ? "1" : "2";
  if (noise(line, t, 5) < 0.25) return "3";
  if (noise(line, t, 9) < 0.15) return "4";
  return "2";
});

// The prompt of an idle agent: "> " and a blinking cursor.
export function screenPrompt(): PixelMap[] {
  return [true, false].map((cursor) =>
    grid(W, H, (x, y) => {
      // ">" at x 1..2, y 4..6
      if ((x === 1 && (y === 4 || y === 6)) || (x === 2 && y === 5)) return "3";
      if (cursor && x === 4 && y >= 4 && y <= 6) return "2";
      if (y === 1 && x >= 1 && x <= 8) return "6";
      return "0";
    }),
  );
}

// A permission dialog: an amber box with a "?" that blinks.
export function screenDialog(): PixelMap[] {
  return [true, false].map((on) =>
    grid(W, H, (x, y) => {
      if (x >= 2 && x <= 9 && y >= 1 && y <= 6) {
        if (!on) return x === 2 || x === 9 || y === 1 || y === 6 ? "8" : "0";
        // "?" in the box
        if ((y === 2 && x >= 5 && x <= 6) || (y === 3 && x === 7) || (y === 4 && x === 6)) return "9";
        return "8";
      }
      return "0";
    }),
  );
}

// A diff under review: added and removed lines, with a highlight that moves
// down the page.
export function screenDiff(): PixelMap[] {
  const kind = ["2", "3", "3", "5", "2", "3", "5", "2"];
  return [0, 2, 4, 6].map((hl) =>
    grid(W, H, (x, y) => {
      if (x === 0) return kind[y] === "3" ? "3" : kind[y] === "5" ? "5" : "0";
      const len = 4 + Math.floor(noise(y, 2, 13) * 6);
      if (x >= 2 && x < 2 + len) return kind[y];
      return y === hl || y === hl + 1 ? "6" : "0";
    }),
  );
}
