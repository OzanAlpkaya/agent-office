// The office's floor plan (ADR 0045): rooms, walls, doorways, furniture and
// the spots characters walk to, in world pixels, for a desk grid. Pure: the
// scene draws it, the path finder walks it and the HTML labels name it.
import * as art from "./art";

export interface Point {
  readonly x: number;
  readonly y: number;
}

export interface Rect {
  readonly x: number;
  readonly y: number;
  readonly w: number;
  readonly h: number;
}

export type RoomId = "lead" | "review" | "kitchen" | "work";
export type FloorKind = "carpet" | "tile" | "kitchen" | "wood";

export interface Room {
  readonly id: RoomId;
  readonly name: string;
  readonly purpose: string; // a sentence for the label's tooltip
  readonly rect: Rect; // the room's floor, from wall to wall
  readonly floor: FloorKind;
  readonly label: Point; // top-left corner of its name label
}

export type Spot = "whiteboard" | "coffee" | "review";

export interface FloorPlan {
  readonly columns: number;
  readonly rows: number;
  readonly stacked: boolean; // rooms on top of each other (3 columns, phones)
  readonly w: number;
  readonly h: number;
  readonly rooms: readonly Room[];
  readonly faces: readonly Rect[]; // back walls seen from the front, doorways cut out
  readonly walls: readonly Rect[]; // partition and outer walls seen from above, doorways cut out
  readonly doorways: readonly Rect[]; // openings through walls (and faces)
  readonly windows: readonly Point[];
  readonly lead: Point; // the Tech Lead's desk cell
  readonly desks: readonly Point[]; // every regular desk cell, row by row
  readonly whiteboard: Point;
  readonly screen: Point; // the review room's wall screen
  readonly table: Rect; // the review room's table
  readonly counter: Point; // the kitchen counter with the coffee machine
  readonly door: Point; // the front door, in the kitchen's back wall
  readonly entrance: Point; // where characters come in and go out
  readonly cooler: Point;
  readonly plants: readonly Point[];
  readonly furniture: readonly Rect[]; // footprints on the floor, besides desks
  readonly spots: Readonly<Record<Spot, readonly Point[]>>; // feet positions
}

// A desk cell, as in Phase 7: desk, chair and the person behind it.
export const cell = { w: 64, h: 52 } as const;
export const margin = 16;
// The part of a desk cell that blocks walking: the desk and the chair behind it.
export const deskFootprint: Rect = { x: 10, y: 4, w: 44, h: 32 };
// Where a character stands up into the aisle right of its desk, relative to the cell.
export const deskExit: Point = { x: 62, y: 28 };

const face = art.wallHeight; // height of a back wall seen from the front
const bandFloor = 60; // floor depth of the rooms along a back wall
const wall = 4; // thickness of a wall seen from above
const corridor = 14; // free floor between the workroom's wall and its first row
const bottom = 10;
const kitchenW = 64;
const doorwayW = 20;
const leadShare = 0.55; // of the width the Tech Lead's and review rooms share

const snap = (v: number): number => Math.round(v / 4) * 4;

export const roomInfo: Readonly<Record<RoomId, { name: string; purpose: string; floor: FloorKind }>> = {
  lead: { name: "Tech Lead", purpose: "The Tech Lead's desk and the whiteboard with the goal's sub-tasks; agents report here.", floor: "carpet" },
  review: { name: "Review room", purpose: "Reviewers read pull requests at the table.", floor: "tile" },
  kitchen: { name: "Kitchen", purpose: "Coffee for idle agents, and the front door.", floor: "kitchen" },
  work: { name: "Workroom", purpose: "Every agent's own desk.", floor: "wood" },
};

const room = (id: RoomId, rect: Rect, labelY: number): Room => ({
  id, rect, label: { x: rect.x + 6, y: labelY + 2 }, ...roomInfo[id],
});

// cutX removes the x range of each gap from the rects it crosses.
function cutX(rects: Rect[], gaps: readonly Rect[]): Rect[] {
  let out = rects;
  for (const g of gaps) {
    const next: Rect[] = [];
    for (const r of out) {
      const crosses = g.x < r.x + r.w && r.x < g.x + g.w && g.y < r.y + r.h && r.y < g.y + g.h;
      if (!crosses) {
        next.push(r);
        continue;
      }
      if (g.x > r.x) next.push({ ...r, w: g.x - r.x });
      if (g.x + g.w < r.x + r.w) next.push({ ...r, x: g.x + g.w, w: r.x + r.w - g.x - g.w });
    }
    out = next;
  }
  return out;
}

const cache = new Map<string, FloorPlan>();

// floorPlan lays out the office for a grid of desks. Three columns give the
// stacked plan (narrow panes, phones); more give the wide one.
export function floorPlan(columns: number, rows: number): FloorPlan {
  const key = `${columns}x${rows}`;
  let plan = cache.get(key);
  if (!plan) {
    plan = build(columns, rows);
    cache.set(key, plan);
  }
  return plan;
}

function build(columns: number, rows: number): FloorPlan {
  const stacked = columns <= 3;
  const W = columns * cell.w + 2 * margin;

  // Bands of rooms along a back wall: a face, then the floor. The wide plan
  // has one band (lead, review, kitchen); the stacked plan two (lead; review,
  // kitchen).
  const shared = W - kitchenW;
  const leadW = stacked ? W : snap(shared * leadShare);
  const bandA = 0;
  const bandB = stacked ? face + bandFloor + wall : 0;
  const floorA = bandA + face;
  const floorB = bandB + face;
  const leadRect: Rect = { x: 0, y: floorA, w: leadW, h: bandFloor };
  const reviewX = stacked ? 0 : leadW;
  const reviewRect: Rect = { x: reviewX, y: floorB, w: W - kitchenW - reviewX, h: bandFloor };
  const kitchenRect: Rect = { x: W - kitchenW, y: floorB, w: kitchenW, h: bandFloor };
  const workTop = floorB + bandFloor + wall;
  const H = workTop + corridor + rows * cell.h + bottom;
  const workRect: Rect = { x: 0, y: workTop, w: W, h: H - workTop };

  const rooms = [
    room("lead", leadRect, bandA),
    room("review", reviewRect, bandB),
    room("kitchen", kitchenRect, bandB),
    room("work", workRect, workTop),
  ];

  // Doorways: each band room opens into what lies below it.
  const leadCx = Math.round(leadW / 2);
  const doorAt = (cx: number, y: number, h: number): Rect => ({ x: snap(cx - doorwayW / 2), y, w: doorwayW, h });
  const bottomOfB = floorB + bandFloor;
  const doorways: Rect[] = [];
  if (stacked) doorways.push(doorAt(leadCx, floorA + bandFloor, wall + face)); // through band B's back wall
  else doorways.push(doorAt(leadCx, bottomOfB, wall));
  doorways.push(doorAt(reviewRect.x + reviewRect.w / 2, bottomOfB, wall));
  doorways.push(doorAt(kitchenRect.x + kitchenRect.w / 2, bottomOfB, wall));

  // Back walls seen from the front.
  const faces = cutX(
    stacked ? [{ x: 0, y: bandA, w: W, h: face }, { x: 0, y: bandB, w: W, h: face }] : [{ x: 0, y: bandA, w: W, h: face }],
    doorways,
  );

  // Walls seen from above: the outer walls, the partitions between rooms of
  // a band (up the back wall too), and the walls under each band.
  const walls: Rect[] = [
    { x: 0, y: 0, w: wall, h: H },
    { x: W - wall, y: 0, w: wall, h: H },
    { x: 0, y: H - wall, w: W, h: wall },
  ];
  const partition = (x: number, top: number): Rect => ({ x: x - wall / 2, y: top, w: wall, h: face + bandFloor });
  if (!stacked) walls.push(partition(leadW, bandA));
  walls.push(partition(kitchenRect.x, bandB));
  const under = (y: number): Rect => ({ x: 0, y, w: W, h: wall });
  if (stacked) walls.push(under(floorA + bandFloor));
  walls.push(under(bottomOfB));
  const cutWalls = cutX(walls, doorways);

  // The Tech Lead's room: the whiteboard on the wall, the desk under it.
  const whiteboard: Point = { x: leadCx - Math.floor(art.whiteboardSize.w / 2), y: bandA + 6 };
  const lead: Point = { x: leadCx - cell.w / 2, y: floorA + 4 };

  // The review room: a screen on the wall, a table with seats behind it.
  const reviewCx = Math.round(reviewRect.x + reviewRect.w / 2);
  const screen: Point = { x: reviewCx - art.wallScreenSize.w / 2, y: bandB + 8 };
  const tw = Math.min(88, snap(reviewRect.w - 34));
  const table: Rect = { x: reviewCx - Math.round(tw / 2), y: floorB + 22, w: tw, h: art.tableHeight };
  const seatCount = Math.max(2, Math.min(4, Math.floor(tw / 22)));
  const seats = Array.from({ length: seatCount }, (_, i) => ({
    x: table.x + Math.round(((i + 0.5) * tw) / seatCount), y: table.y - 6,
  }));

  // The kitchen: the counter on the left, the front door on the right.
  const counter: Point = { x: kitchenRect.x + 6, y: floorB - art.counterTop };
  const door: Point = { x: kitchenRect.x + kitchenRect.w - art.door.w - 6, y: floorB - art.door.h };
  const entrance: Point = { x: door.x + Math.floor(art.door.w / 2), y: floorB + 6 };
  const coffee = [
    { x: counter.x + 7, y: floorB + 26 },
    { x: counter.x + 21, y: floorB + 26 },
    { x: counter.x + 14, y: floorB + 40 },
  ];

  // Standing places at the whiteboard: left of the lead's desk and right of
  // its exit lane.
  const whiteboardSpots = [
    { x: leadCx - 32, y: floorA + 22 },
    { x: leadCx + 44, y: floorA + 22 },
    { x: leadCx - 48, y: floorA + 22 },
  ];

  const desks: Point[] = [];
  for (let r = 0; r < rows; r++) {
    for (let c = 0; c < columns; c++) desks.push({ x: margin + c * cell.w, y: workTop + corridor + r * cell.h });
  }

  const windows: Point[] = [];
  const fill = (from: number, to: number, y: number): void => {
    // windows evenly in [from, to), 32 wide, at least 8 apart
    const n = Math.floor((to - from + 8) / 40);
    if (n <= 0) return;
    const gap = Math.floor((to - from - n * art.windowMap.w) / (n + 1));
    for (let i = 0; i < n; i++) windows.push({ x: from + gap + i * (art.windowMap.w + gap), y });
  };
  fill(wall + 2, whiteboard.x - 4, bandA + 8);
  fill(whiteboard.x + art.whiteboardSize.w + 4, leadRect.x + leadRect.w - wall - 2, bandA + 8);
  fill(reviewRect.x + wall + 2, screen.x - 4, bandB + 8);
  fill(screen.x + art.wallScreenSize.w + 4, reviewRect.x + reviewRect.w - wall - 2, bandB + 8);

  const cooler: Point = { x: W - wall - art.waterCooler.w - 2, y: H - wall - art.waterCooler.h - 2 };
  const plants: Point[] = [
    { x: wall + 2, y: floorA + bandFloor - art.plant.h - 2 },
    { x: wall + 2, y: H - wall - art.plant.h - 2 },
  ];
  const footprint = (p: Point, w: number, h: number, below: number): Rect => ({ x: p.x, y: p.y + h - below, w, h: below });
  const furniture: Rect[] = [
    { ...table, h: table.h - 2 },
    { x: counter.x, y: floorB, w: art.counter.w, h: art.counter.h - art.counterTop },
    footprint(cooler, art.waterCooler.w, art.waterCooler.h, 8),
    ...plants.map((p) => footprint(p, art.plant.w, art.plant.h, 6)),
  ];

  return {
    columns, rows, stacked, w: W, h: H, rooms, faces, walls: cutWalls, doorways, windows, lead, desks, whiteboard, screen,
    table, counter, door, entrance, cooler, plants, furniture,
    spots: { whiteboard: whiteboardSpots, coffee, review: seats },
  };
}

export function roomAt(plan: FloorPlan, p: Point): Room | undefined {
  return plan.rooms.find((r) => p.x >= r.rect.x && p.x < r.rect.x + r.rect.w && p.y >= r.rect.y && p.y < r.rect.y + r.rect.h);
}
