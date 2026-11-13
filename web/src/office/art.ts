import type { Kind, TaskStatus } from "../api";
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

// Legs, drawn only while a character walks out of the office (an exited
// agent, ADR 0024): two frames of a stride, joined below the body.
export const legs: readonly PixelMap[] = [
  pixelMap([
    "...knnnnnnnnk...",
    "...knnnkknnnk...",
    "...knnk..knnk...",
    "...knnk..knnk...",
    "...kkkk..kkkk...",
  ]),
  pixelMap([
    "...knnnnnnnnk...",
    "..knnnk..knnnk..",
    "..knnk....knnk..",
    ".knnk......knnk.",
    ".kkkk......kkkk.",
  ]),
];

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

// A task status's colours: the whiteboard's sticky notes and counts, and the
// office bar's summary (Phase 20).
export const statusColors: Readonly<Record<TaskStatus, Swatch>> = {
  todo: { base: 0xf2d16b, shade: 0xc9a63c },
  in_progress: { base: 0x8ab4f8, shade: 0x5a8de0 },
  review: { base: 0xc49bff, shade: 0x9b6dde },
  done: { base: 0x8fd694, shade: 0x5cb860 },
  blocked: { base: 0xf28b82, shade: 0xd05a50 },
};

// The board summary on the whiteboard (Phase 20): per column its count in a
// 3×5 pixel font, in the column's colour, on the line where the title was.
export const countLine = { x: 4, y: 3, w: 68 } as const;
export const glyphs: Readonly<Record<string, readonly string[]>> = {
  "0": ["###", "#.#", "#.#", "#.#", "###"],
  "1": [".#.", "##.", ".#.", ".#.", "###"],
  "2": ["###", "..#", "###", "#..", "###"],
  "3": ["###", "..#", ".##", "..#", "###"],
  "4": ["#.#", "#.#", "###", "..#", "..#"],
  "5": ["###", "#..", "###", "..#", "###"],
  "6": ["###", "#..", "###", "#.#", "###"],
  "7": ["###", "..#", ".#.", ".#.", ".#."],
  "8": ["###", "#.#", "###", "#.#", "###"],
  "9": ["###", "#.#", "###", "..#", "###"],
  "+": ["...", ".#.", "###", ".#.", "..."],
};

// countText is a count as the whiteboard writes it: up to 99, then "99+".
export const countText = (n: number): string => (n > 99 ? "99+" : String(Math.max(0, Math.floor(n))));

// countPixels lays out the counts on the count line: the pixels of each
// count's digits (3×5, 1 apart), groups as far apart as fits, relative to
// the count line.
export interface CountPixels {
  ink: { group: number; x: number; y: number }[];
  starts: number[]; // x where each group starts
  width: number;
}
export function countPixels(counts: readonly number[]): CountPixels {
  const texts = counts.map(countText);
  const used = texts.reduce((n, t) => n + t.length * 4 - 1, 0);
  const gap = counts.length > 1 ? Math.max(3, Math.min(9, Math.floor((countLine.w - used) / (counts.length - 1)))) : 0;
  const out: CountPixels = { ink: [], starts: [], width: 0 };
  let x = 0;
  texts.forEach((t, group) => {
    if (group > 0) x += gap;
    out.starts.push(x);
    [...t].forEach((ch, k) => {
      if (k > 0) x += 1;
      glyphs[ch].forEach((row, y) => {
        for (let gx = 0; gx < 3; gx++) if (row[gx] === "#") out.ink.push({ group, x: x + gx, y });
      });
      x += 3;
    });
  });
  out.width = x;
  return out;
}

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

// ---------------------------------------------------------------- Office v2 (ADR 0045)

// The back of a character, for walking away from the viewer: the face is
// covered by hair; the hair style is still drawn on top.
export const bodyBack = pixelMap(
  body.rows.map((row, y) =>
    y >= 2 && y <= 9
      ? row.replace(/[sem]/g, (_c, x: number) => (y >= 3 && (x === 4 || x === 11) ? "H" : "h"))
      : row,
  ),
);

// Floors per room: a carpet in the Tech Lead's room, tiles in the review room
// and the kitchen. The workroom keeps the wooden planks (floorTile).
export const carpetTile = grid(16, 16, (x, y) => {
  if ((x + y) % 8 === 0 || (x - y + 16) % 8 === 0) return "1";
  return noise(x, y, 21) < 0.08 ? "2" : "0";
});
export const carpetPalette: Palette = { "0": 0x3a2f4f, "1": 0x433758, "2": 0x342a47 };

export const tileFloor = grid(16, 16, (x, y) => {
  if (x === 15 || y === 15) return "2";
  return ((x >> 3) + (y >> 3)) % 2 === 0 ? "0" : "1";
});
export const tilePalette: Palette = { "0": 0x34405a, "1": 0x3a4762, "2": 0x2a3349 };
export const kitchenPalette: Palette = { "0": 0x6b5a4a, "1": 0x5e4f41, "2": 0x4a3e33 };

// Partition walls seen from above, drawn by the scene as rectangles: the top
// of the wall and its darker edge.
export const wallTopColor = 0x4a5478;
export const wallEdgeColor = 0x1c2033;

// A meeting table, w pixels wide: a top seen from slightly above and a front
// panel on two legs, with two sheets of paper on it.
export const tableHeight = 16;
export const table = (w: number): PixelMap =>
  grid(w, tableHeight, (x, y) => {
    const edgeX = x === 0 || x === w - 1;
    if (y === 0) return edgeX ? "." : "q";
    if (y <= 6) {
      if (edgeX) return "q";
      const px = x % 22;
      if (y >= 2 && y <= 5 && px >= 6 && px <= 12) return y === 3 && px > 6 && px < 12 ? "g" : "w"; // paper
      return y === 6 ? "E" : "d";
    }
    if (y === 7) return "q";
    if (y >= 14) return x < 3 || x >= w - 3 ? "q" : "."; // legs
    return edgeX ? "q" : "D";
  });

// A screen on the review room's wall, showing a diff.
export const wallScreenSize = { w: 32, h: 20 } as const;
export const wallScreen = grid(wallScreenSize.w, wallScreenSize.h, (x, y) => {
  const { w, h } = wallScreenSize;
  if (x === 0 || y === 0 || x === w - 1 || y === h - 1) return "k";
  if (x === 1 || y === 1 || x === w - 2 || y === h - 2) return "f";
  const line = Math.floor((y - 3) / 2);
  if (y < 3 || (y - 3) % 2 === 1 || line > 6) return "0";
  const kind = ["2", "3", "3", "5", "2", "3", "5"][line];
  if (x === 3) return kind === "2" ? "0" : kind;
  const len = 8 + Math.floor(noise(line, 3, 17) * 16);
  return x >= 5 && x < 5 + len && x < w - 3 ? kind : "0";
});
export const wallScreenPalette: Palette = {
  ...basePalette, "0": 0x111a2a, "2": 0xc0caf5, "3": 0x9ece6a, "5": 0xf7768e,
};

// The kitchen counter with the coffee machine and a mug, standing against
// the back wall: the machine rises over the wall, the counter stands on the
// floor (its top at row 14, its front from row 20).
export const counterTop = 14;
export const counter = pixelMap([
  "..kkkkkkkkkk................",
  "..knnnnnnnnk................",
  "..knGGGGGGnk................",
  "..knGkkkkGnk................",
  "..knGkrrkGnk................",
  "..knGkkkkGnk................",
  "..knnnnnnnnk................",
  "..kn......nk................",
  "..kn.kllk.nk......kkkk......",
  "..kn.kLLk.nk......kyykkk....",
  "..kn.kLLk.nk......kyyk.k....",
  "..knnkkkknnk......kyykkk....",
  "..knnnnnnnnk......kkkk......",
  "..kkkkkkkkkk................",
  "kkkkkkkkkkkkkkkkkkkkkkkkkkkk",
  "kggggggggggggggggggggggggggk",
  "kggggggggggggggggggggggggggk",
  "kgGGGGGGGGGGGGGGGGGGGGGGGGgk",
  "kGGGGGGGGGGGGGGGGGGGGGGGGGGk",
  "kkkkkkkkkkkkkkkkkkkkkkkkkkkk",
  "kDDDDDDDDDDDDqDDDDDDDDDDDDDk",
  "kDDDDDDDDDDDDqDDDDDDDDDDDDDk",
  "kDDDDDDDDDDuDqDuDDDDDDDDDDDk",
  "kDDDDDDDDDDuDqDuDDDDDDDDDDDk",
  "kDDDDDDDDDDDDqDDDDDDDDDDDDDk",
  "kDDDDDDDDDDDDqDDDDDDDDDDDDDk",
  "kDDDDDDDDDDDDqDDDDDDDDDDDDDk",
  "kDDDDDDDDDDDDqDDDDDDDDDDDDDk",
  "kqqqqqqqqqqqqqqqqqqqqqqqqqqk",
  "kkkkkkkkkkkkkkkkkkkkkkkkkkkk",
]);

// ---------------------------------------------------------------- more rooms (ADR 0045, review)

// Blueprint colours: paper, lines, and a darker edge.
export const blueprintPalette: Palette = { ...basePalette, B: 0x2f5fa8, b: 0x9cc4f2, N: 0x1f3f75 };

// The architect's drafting table: a tilted board with a blueprint, on two legs.
export const draftingSize = { w: 44, h: 22 } as const;
export const draftingTable = grid(draftingSize.w, draftingSize.h, (x, y) => {
  const { w } = draftingSize;
  if (y <= 11) {
    // the tilted board: wider at the front
    const inset = Math.max(0, 3 - Math.floor(y / 3));
    if (x < inset || x >= w - inset) return ".";
    if (y === 0 || x === inset || x === w - inset - 1) return "q";
    if (y === 11) return "q";
    // the blueprint on it
    if (x > inset + 2 && x < w - inset - 3 && y >= 2 && y <= 9) {
      if (y === 2 || y === 9 || x === inset + 3 || x === w - inset - 4) return "N";
      if ((y === 4 && x > 10 && x < 30) || (x === 18 && y > 4 && y < 9) || (y === 7 && x > 22 && x < 34)) return "b";
      return "B";
    }
    return y === 10 ? "E" : "d";
  }
  if (y === 12) return x < 4 || x >= w - 4 ? "." : "q";
  if (x === 8 || x === 9 || x === w - 10 || x === w - 9) return "n"; // legs
  if (y === 16 && x > 8 && x < w - 9) return "n"; // footrest
  return ".";
});

// Blueprints pinned to the planning room's wall.
export const wallBlueprint = grid(26, 18, (x, y) => {
  if (x === 0 || y === 0 || x === 25 || y === 17) return "N";
  if (y === 1 && (x === 3 || x === 22)) return "r"; // pins
  if ((y === 5 && x > 3 && x < 20) || (x === 12 && y > 5 && y < 14) || (y === 11 && x > 14 && x < 23)) return "b";
  if ((x === 4 || x === 20) && y > 5 && y < 14) return "b";
  return "B";
});

// QA's test bench: a grey bench (the table shape in metal) and a test rig on it.
export const bench = (w: number): PixelMap => table(w);
export const benchPalette: Palette = { ...basePalette, d: 0x9aa3b5, E: 0x7d8496, D: 0x5d6475, q: 0x2e3340, w: 0xd8e4d0, g: 0x8fd694 };
export const testRig = pixelMap([
  ".kkkkkkkkkkkk.",
  "kffffffffffffk",
  "kfxppxxrrxppfk",
  "kfxxxxxxxxxxfk",
  "kfxppxxppxppfk",
  "kffffffffffffk",
  ".kkkkkkkkkkkk.",
  "...kGGGGGGk...",
]);

// A board of test results on the lab's wall: passing runs, one red.
export const testBoard = grid(28, 18, (x, y) => {
  if (x === 0 || y === 0 || x === 27 || y === 17) return "k";
  if (x === 1 || y === 1 || x === 26 || y === 16) return "g";
  if (y >= 4 && y <= 13 && x >= 4 && x <= 23 && (x - 4) % 4 < 3 && (y - 4) % 3 < 2) {
    return x >= 16 && x <= 18 && y >= 10 && y <= 11 ? "r" : "p";
  }
  if (y === 2 && x >= 4 && x <= 14) return "n";
  return "w";
});

// The reception counter in the lobby: a raised wooden front and a bell.
export const receptionHeight = 18;
export const reception = (w: number): PixelMap =>
  grid(w, receptionHeight, (x, y) => {
    const edgeX = x === 0 || x === w - 1;
    if (y === 0) return edgeX ? "." : "k";
    if (y <= 3) {
      if (edgeX) return "k";
      if (y === 1 && x >= w - 7 && x <= w - 5) return "u"; // the bell
      return y === 3 ? "G" : "g";
    }
    if (y === 4) return "k";
    if (y === receptionHeight - 1) return "k";
    if (edgeX) return "k";
    if (y === 8 && x > 2 && x < w - 3) return "q";
    return "D";
  });

// Props for the queue at the user's door: a rolled-up plan and a note.
export const blueprintRoll = pixelMap([
  "NNNNNNNN",
  "NBbBBbBN",
  "NNNNNNNN",
]);
export const note = pixelMap([
  "kkkk",
  "kwwk",
  "kgwk",
  "kwwk",
  "kkkk",
]);

// Shirts for roles the office has no colour for (a user's role pack, e.g.
// docs or security): a role's colour comes from a hash of its name.
export const roleShirts: readonly Swatch[] = [
  { base: 0xd96aa8, shade: 0xb04f88 }, // pink
  { base: 0xe0c14a, shade: 0xb89c30 }, // yellow
  { base: 0x7a8c3a, shade: 0x5e6e28 }, // olive
  { base: 0xa070d0, shade: 0x8050b0 }, // orchid
  { base: 0xe08a4a, shade: 0xb86a30 }, // orange
  { base: 0x5ab0d9, shade: 0x3a90b8 }, // sky
  { base: 0xb0705a, shade: 0x8e5440 }, // clay
  { base: 0x8a9aa8, shade: 0x6a7a88 }, // slate
];

// Floors of the new rooms: a blue carpet for planning, light tiles in the lab,
// a warm rug in the user's office.
export const planningPalette: Palette = { "0": 0x26375a, "1": 0x2d4068, "2": 0x22314f };
export const labPalette: Palette = { "0": 0x5b6475, "1": 0x646e80, "2": 0x4a5263 };
export const userPalette: Palette = { "0": 0x5a2f3a, "1": 0x683844, "2": 0x4e2832 };
