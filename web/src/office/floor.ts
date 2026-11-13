// The office's floor plan (ADR 0045): rooms, walls, doorways, furniture,
// stations and the spots characters walk to, in world pixels, for a desk
// grid. Pure: the scene draws it, the path finder walks it and the HTML
// labels name it.
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

export type RoomId = "lead" | "planning" | "user" | "review" | "qa" | "kitchen" | "lobby" | "work";
export type FloorKind = "carpet" | "tile" | "kitchen" | "wood" | "blue" | "lab" | "rug";

export interface Room {
  readonly id: RoomId;
  readonly name: string;
  readonly purpose: string; // a sentence for the label's tooltip
  readonly rect: Rect; // the room's floor, from wall to wall
  readonly floor: FloorKind;
  readonly label: Point; // top-left corner of its name label
}

// Spots are places characters walk to on errands; queue is the line at the
// user's door.
export type Spot = "whiteboard" | "coffee" | "review" | "queue";

// A station is a role's own place outside the workroom: the architect's
// drafting table, QA's test bench, the assistant's reception counter. Its
// agent sits behind the furniture, like reviewers at the review table.
export type StationKind = "architect" | "qa" | "assistant";
export const stationKinds: readonly StationKind[] = ["architect", "qa", "assistant"];

export interface Station {
  readonly kind: StationKind;
  readonly furniture: Rect; // drawn here; its top is what the agent sits behind
  readonly seat: Point; // feet, behind the furniture
  readonly sitY: number; // top of the seated body
  readonly nameplate: Point; // centre of the agent's nameplate
}

export interface FloorPlan {
  readonly columns: number;
  readonly rows: number;
  readonly stacked: boolean; // rooms on top of each other (3 columns, phones)
  // A project's own layout (Phase 18, ADR 0057): fixed rooms and desks. The
  // default plan grows with the team; a custom one has capacity desks, and
  // may lack the optional rooms (their furniture and stations are absent).
  readonly custom?: boolean;
  readonly capacity?: number;
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
  readonly screen?: Point; // the review room's wall screen
  readonly table?: Rect; // the review room's table
  readonly counter?: Point; // the kitchen counter with the coffee machine
  readonly door: Point; // the front door, in the lobby's back wall
  readonly entrance: Point; // where characters come in and go out
  readonly userDesk: Point; // the user's own desk cell (nobody sits there)
  readonly userDoor: Rect; // the doorway of the user's office
  readonly queueSign: Point; // where the "N waiting" sign goes, by the user's door
  readonly blueprints?: Point; // pinned on the planning room's wall
  readonly testBoard?: Point; // on the QA lab's wall
  readonly stations: Readonly<Partial<Record<StationKind, Station>>>;
  readonly cooler: Point;
  readonly plants: readonly Point[];
  readonly furniture: readonly Rect[]; // footprints on the floor, besides desks
  readonly spots: Readonly<Record<Spot, readonly Point[]>>; // feet positions
}

// FullPlan is the default plan: every room, so every piece of furniture.
export type FullPlan = FloorPlan & {
  readonly screen: Point; readonly table: Rect; readonly counter: Point; readonly blueprints: Point; readonly testBoard: Point;
  readonly stations: Readonly<Record<StationKind, Station>>;
};

// A desk cell, as in Phase 7: desk, chair and the person behind it.
export const cell = { w: 64, h: 52 } as const;
export const margin = 16;
// The part of a desk cell that blocks walking: the desk and the chair behind it.
export const deskFootprint: Rect = { x: 10, y: 4, w: 44, h: 32 };
// Where a character stands up into the aisle right of its desk, relative to the cell.
export const deskExit: Point = { x: 62, y: 28 };

export const face = art.wallHeight; // height of a back wall seen from the front
const bandFloor = 60; // floor depth of the rooms along a back wall
export const wall = 4; // thickness of a wall seen from above
const band = face + bandFloor + wall;
export const corridor = 14; // free floor between the workroom's wall and its first row
export const bottom = 10;
export const doorwayW = 20;
export const queueStep = 12;

export const snap = (v: number): number => Math.round(v / 4) * 4;

export const roomInfo: Readonly<Record<RoomId, { name: string; purpose: string; floor: FloorKind }>> = {
  lead: { name: "Tech Lead", purpose: "The Tech Lead's desk and the whiteboard with the goal's sub-tasks; agents report here.", floor: "carpet" },
  planning: { name: "Planning", purpose: "The architect drafts the plan here, and takes it to your door for approval.", floor: "blue" },
  user: { name: "Your office", purpose: "Yours. Agents waiting on you queue at this door: as many as the Approvals count.", floor: "rug" },
  review: { name: "Review room", purpose: "Reviewers read pull requests at the table.", floor: "tile" },
  qa: { name: "QA lab", purpose: "QA merges the reported branches and runs the tests at the bench.", floor: "lab" },
  kitchen: { name: "Kitchen", purpose: "Coffee for idle agents.", floor: "kitchen" },
  lobby: { name: "Lobby", purpose: "The front door, and the assistant at reception.", floor: "tile" },
  work: { name: "Workroom", purpose: "Every other agent's own desk, roles from your role packs included.", floor: "wood" },
};

// Room widths per band: the minimum, and how the rest of the width is shared.
interface Spec {
  id: RoomId;
  min: number;
  grow: number;
}

// Wide: the decision makers along the back wall (planning, Tech Lead, your
// office), the rooms they open into below. Your office sits over the lobby,
// whose front door and reception are by the queue to your door.
const wideBands: Spec[][] = [
  [{ id: "planning", min: 104, grow: 1 }, { id: "lead", min: 128, grow: 2 }, { id: "user", min: 96, grow: 0 }],
  [{ id: "qa", min: 88, grow: 1 }, { id: "review", min: 104, grow: 2 }, { id: "kitchen", min: 72, grow: 0 }, { id: "lobby", min: 112, grow: 0 }],
];
// Stacked (phones): three bands of two or three rooms.
const stackedBands: Spec[][] = [
  [{ id: "planning", min: 96, grow: 0 }, { id: "lead", min: 128, grow: 1 }],
  [{ id: "review", min: 88, grow: 0 }, { id: "qa", min: 72, grow: 1 }, { id: "user", min: 64, grow: 0 }],
  [{ id: "kitchen", min: 96, grow: 0 }, { id: "lobby", min: 128, grow: 1 }],
];

function widths(specs: readonly Spec[], W: number): number[] {
  const min = specs.reduce((n, s) => n + s.min, 0);
  const grow = specs.reduce((n, s) => n + s.grow, 0);
  const extra = W - min;
  const out = specs.map((s) => s.min + (grow > 0 ? snap((extra * s.grow) / grow) : 0));
  // the last growing room takes what rounding left
  const diff = W - out.reduce((n, x) => n + x, 0);
  const last = specs.map((s) => s.grow > 0).lastIndexOf(true);
  out[last === -1 ? out.length - 1 : last] += diff;
  return out;
}

export const minWidth = (stacked: boolean): number =>
  Math.max(...(stacked ? stackedBands : wideBands).map((b) => b.reduce((n, s) => n + s.min, 0)));

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

const cache = new Map<string, FullPlan>();

// floorPlan lays out the office for a grid of desks. Three columns give the
// stacked plan (narrow panes, phones); more give the wide one.
// extra deepens the workroom below its desks (world pixels), so the office
// covers a tall pane without empty desks (Phase 18 review).
export function floorPlan(columns: number, rows: number, extra = 0): FullPlan {
  const key = `${columns}x${rows}+${extra}`;
  let plan = cache.get(key);
  if (!plan) {
    plan = build(columns, rows, extra);
    cache.set(key, plan);
  }
  return plan;
}

function build(columns: number, rows: number, extra: number): FullPlan {
  const stacked = columns <= 3;
  const bands = stacked ? stackedBands : wideBands;
  const W = Math.max(columns * cell.w + 2 * margin, minWidth(stacked));

  // Rooms, band by band: a back wall (face), the floor, a wall below.
  const rect: Partial<Record<RoomId, Rect>> = {};
  const bandOf: Partial<Record<RoomId, number>> = {};
  bands.forEach((specs, b) => {
    let x = 0;
    widths(specs, W).forEach((w, i) => {
      rect[specs[i].id] = { x, y: b * band + face, w, h: bandFloor };
      bandOf[specs[i].id] = b;
      x += w;
    });
  });
  const R = (id: RoomId): Rect => rect[id] as Rect;
  const workTop = bands.length * band;
  const H = workTop + corridor + rows * cell.h + bottom + extra;
  const workRect: Rect = { x: 0, y: workTop, w: W, h: H - workTop };
  const ids: RoomId[] = [...bands.flat().map((s) => s.id), "work"];
  const rooms: Room[] = ids.map((id) => {
    const r = id === "work" ? workRect : R(id);
    const top = id === "work" ? workTop : r.y - face;
    return { id, rect: r, label: { x: r.x + 6, y: top + 2 }, ...roomInfo[id] };
  });

  // The lobby: the user's door on the left of its back wall, the front door on
  // the right, reception in between.
  const lobby = R("lobby");
  const user = R("user");
  const userDoorX = snap(Math.max(lobby.x, user.x) + 8);
  const queueX = userDoorX + doorwayW / 2;

  // Doorways: every band room opens into what lies below it, through that
  // room's back wall when there is a band below. The user's office opens into
  // the lobby; the lobby opens into the workroom under the user's door.
  const doorways: Rect[] = [];
  const below = (r: Rect): Rect[] => {
    const b = bandOf[ids.find((id) => rect[id] === r) as RoomId] as number;
    return b + 1 < bands.length ? bands[b + 1].map((s) => R(s.id)) : [workRect];
  };
  for (const specs of bands) {
    for (const s of specs) {
      const r = R(s.id);
      const deeper = below(r)[0] !== workRect;
      const h = deeper ? wall + face : wall;
      const y = r.y + bandFloor;
      if (s.id === "user") {
        doorways.push({ x: userDoorX, y, w: doorwayW, h });
        continue;
      }
      if (s.id === "lobby") {
        doorways.push({ x: userDoorX, y, w: doorwayW, h: wall });
        continue;
      }
      // the widest overlap with a room below, never into the user's office
      let best: [number, number] | undefined;
      for (const t of below(r)) {
        if (t === user) continue;
        const a = Math.max(r.x, t.x) + 10, z = Math.min(r.x + r.w, t.x + t.w) - 10;
        if (z - a >= doorwayW && (!best || z - a > best[1] - best[0])) best = [a, z];
      }
      if (!best) best = [r.x + 10, r.x + r.w - 10];
      doorways.push({ x: snap((best[0] + best[1]) / 2 - doorwayW / 2), y, w: doorwayW, h });
    }
  }

  // Back walls seen from the front.
  const faces = cutX(bands.map((_, b) => ({ x: 0, y: b * band, w: W, h: face })), doorways);

  // Walls seen from above: the outer walls, partitions between the rooms of a
  // band (up the back wall too), and the wall under each band.
  const walls: Rect[] = [
    { x: 0, y: 0, w: wall, h: H },
    { x: W - wall, y: 0, w: wall, h: H },
    { x: 0, y: H - wall, w: W, h: wall },
  ];
  bands.forEach((specs, b) => {
    specs.slice(1).forEach((s) => walls.push({ x: R(s.id).x - wall / 2, y: b * band, w: wall, h: face + bandFloor }));
    walls.push({ x: 0, y: b * band + face + bandFloor, w: W, h: wall });
  });
  const cutWalls = cutX(walls, doorways);

  // The Tech Lead's room: the whiteboard on the wall, the desk under it.
  const lead = R("lead");
  const leadCx = Math.round(lead.x + lead.w / 2);
  const whiteboard: Point = { x: leadCx - Math.floor(art.whiteboardSize.w / 2), y: lead.y - face + 6 };
  const leadDesk: Point = { x: leadCx - cell.w / 2, y: lead.y + 4 };
  const whiteboardSpots = [
    { x: leadCx - 32, y: lead.y + 22 },
    { x: leadCx + 44, y: lead.y + 22 },
    { x: leadCx - 48, y: lead.y + 22 },
  ];

  // The review room: a screen on the wall, a table with seats behind it.
  const review = R("review");
  const reviewCx = Math.round(review.x + review.w / 2);
  const screen: Point = { x: reviewCx - art.wallScreenSize.w / 2, y: review.y - face + 8 };
  const tw = Math.min(88, snap(review.w - 34));
  const table: Rect = { x: reviewCx - Math.round(tw / 2), y: review.y + 22, w: tw, h: art.tableHeight };
  const seatCount = Math.max(2, Math.min(4, Math.floor(tw / 22)));
  const seats = Array.from({ length: seatCount }, (_, i) => ({
    x: table.x + Math.round(((i + 0.5) * tw) / seatCount), y: table.y - 6,
  }));

  // The kitchen: the counter with the coffee machine.
  const kitchen = R("kitchen");
  const counter: Point = { x: kitchen.x + 8, y: kitchen.y - art.counterTop };
  const coffee = [
    { x: counter.x + 7, y: kitchen.y + 26 },
    { x: counter.x + 21, y: kitchen.y + 26 },
    { x: counter.x + 14, y: kitchen.y + 40 },
  ];

  // The lobby: the front door at the right of the back wall.
  const door: Point = { x: lobby.x + lobby.w - art.door.w - 8, y: lobby.y - art.door.h };
  const entrance: Point = { x: door.x + Math.floor(art.door.w / 2), y: lobby.y + 6 };

  // A station: furniture of width w centred at cx, its top at y; its agent
  // sits behind it.
  const station = (kind: StationKind, cx: number, y: number, w: number, h: number): Station => {
    const furniture: Rect = { x: Math.round(cx - w / 2), y, w, h };
    return {
      kind, furniture, seat: { x: Math.round(cx), y: y - 6 }, sitY: y - 15,
      nameplate: { x: Math.round(cx), y: y + h + 7 },
    };
  };
  const planning = R("planning");
  const qa = R("qa");
  // the bench leaves an aisle on both sides: the lab is a way through
  const benchW = Math.min(48, snap(qa.w - 40));
  // Reception: beside the queue, on whichever side has room (between the queue
  // and the front door's way in, or between the queue and the left wall).
  const rightRoom = door.x - (queueX + 10) - 10;
  const leftRoom = queueX - 10 - (lobby.x + wall + 8);
  const recW = Math.min(36, Math.max(rightRoom, leftRoom));
  const recCx = rightRoom >= leftRoom ? queueX + 10 + 4 + recW / 2 : queueX - 10 - 4 - recW / 2;
  const stations: Record<StationKind, Station> = {
    architect: station("architect", planning.x + planning.w / 2, planning.y + 20, art.draftingSize.w, art.draftingSize.h),
    qa: station("qa", qa.x + qa.w / 2, qa.y + 22, benchW, art.tableHeight),
    assistant: station("assistant", recCx, lobby.y + 24, recW, art.receptionHeight),
  };

  // The queue at the user's door: down through the lobby, out of its doorway
  // into the workroom's corridor, then along it to the left.
  const queue: Point[] = [];
  for (let y = lobby.y + 10; y <= lobby.y + bandFloor - 10; y += queueStep) queue.push({ x: queueX, y });
  for (let x = queueX; x >= wall + 12; x -= queueStep) queue.push({ x, y: workTop + 8 });
  for (let x = queueX + queueStep; x <= W - wall - 12; x += queueStep) queue.push({ x, y: workTop + 8 });

  // The user's own office: a desk with a chair nobody uses, under a window.
  const userDesk: Point = { x: user.x + Math.round((user.w - cell.w) / 2) + 6, y: user.y + 2 };
  const userDoor = doorways.find((d) => d.x === userDoorX && d.y === user.y + bandFloor) as Rect;
  const queueSign: Point = { x: userDoorX + doorwayW + 2, y: user.y + bandFloor - 12 };

  const deskMargin = Math.round((W - columns * cell.w) / 2 / 4) * 4;
  const desks: Point[] = [];
  for (let r = 0; r < rows; r++) {
    for (let c = 0; c < columns; c++) desks.push({ x: deskMargin + c * cell.w, y: workTop + corridor + r * cell.h });
  }

  const windows: Point[] = [];
  const fill = (from: number, to: number, y: number): void => {
    // windows evenly in [from, to), 32 wide, at least 8 apart
    const n = Math.floor((to - from + 8) / 40);
    if (n <= 0) return;
    const gap = Math.floor((to - from - n * art.windowMap.w) / (n + 1));
    for (let i = 0; i < n; i++) windows.push({ x: from + gap + i * (art.windowMap.w + gap), y });
  };
  const blueprints: Point = { x: Math.round(planning.x + planning.w / 2 - 13), y: planning.y - face + 10 };
  const testBoard: Point = { x: Math.round(qa.x + qa.w / 2 - 14), y: qa.y - face + 10 };
  const wallFill = (r: Rect, item: Point, itemW: number): void => {
    fill(r.x + wall + 2, item.x - 4, r.y - face + 8);
    fill(item.x + itemW + 4, r.x + r.w - wall - 2, r.y - face + 8);
  };
  // Windows only on outside walls (Phase 20): the first band's back walls.
  // The rooms of the other bands, and the workroom, have rooms behind theirs.
  const outside = (id: RoomId): boolean => bandOf[id] === 0;
  if (outside("lead")) wallFill(lead, whiteboard, art.whiteboardSize.w);
  if (outside("review")) wallFill(review, screen, art.wallScreenSize.w);
  if (outside("planning")) wallFill(planning, blueprints, 26);
  if (outside("qa")) wallFill(qa, testBoard, 28);
  if (outside("user")) fill(user.x + wall + 2, user.x + user.w - wall - 2, user.y - face + 8);

  const cooler: Point = { x: W - wall - art.waterCooler.w - 2, y: H - wall - art.waterCooler.h - 2 };
  const plants: Point[] = [
    { x: wall + 2, y: lead.y + bandFloor - art.plant.h - 2 },
    { x: wall + 2, y: H - wall - art.plant.h - 2 },
    { x: lobby.x + lobby.w - wall - art.plant.w - 2, y: lobby.y + bandFloor - art.plant.h - 2 },
  ];
  const footprint = (p: Point, w: number, h: number, below: number): Rect => ({ x: p.x, y: p.y + h - below, w, h: below });
  const furniture: Rect[] = [
    { ...table, h: table.h - 2 },
    { x: counter.x, y: kitchen.y, w: art.counter.w, h: art.counter.h - art.counterTop },
    ...stationKinds.map((k) => ({ ...stations[k].furniture, h: stations[k].furniture.h - 2 })),
    { x: userDesk.x + deskFootprint.x, y: userDesk.y + deskFootprint.y, w: deskFootprint.w, h: deskFootprint.h },
    footprint(cooler, art.waterCooler.w, art.waterCooler.h, 8),
    ...plants.map((p) => footprint(p, art.plant.w, art.plant.h, 6)),
  ];

  return {
    columns, rows, stacked, w: W, h: H, rooms, faces, walls: cutWalls, doorways, windows, lead: leadDesk, desks, whiteboard,
    screen, table, counter, door, entrance, userDesk, userDoor, queueSign, blueprints, testBoard, stations, cooler, plants,
    furniture, spots: { whiteboard: whiteboardSpots, coffee, review: seats, queue },
  };
}

export function roomAt(plan: FloorPlan, p: Point): Room | undefined {
  return plan.rooms.find((r) => p.x >= r.rect.x && p.x < r.rect.x + r.rect.w && p.y >= r.rect.y && p.y < r.rect.y + r.rect.h);
}
