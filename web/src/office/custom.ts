// A project's own office layout as a floor plan (Phase 18, ADR 0057). The
// daemon parses and validates the YAML (internal/office); this builds the
// same kind of plan as floor.ts from its rooms on a grid of 16-pixel tiles:
// every room has its back wall at its top and a wall at its bottom, doors
// are cut in the middle of two rooms' shared wall, and the furniture of each
// room is placed as in the default plan. Pure.
import type { LayoutRoom, OfficeLayout } from "../api";
import * as art from "./art";
import {
  type FloorPlan, type Point, type Rect, type Room, type RoomId, type Station, type StationKind, bottom, cell, corridor,
  deskFootprint, doorwayW, face, margin, queueStep, roomInfo, snap, wall,
} from "./floor";

export const tile = 16;
const doorEdge = 10; // floor kept on each side of a doorway along a back wall
const maxQueue = 40;

const cache = new Map<string, FloorPlan>();

// customPlan is the floor plan of a layout; the same layout gives the same
// plan object, so the path finder's grid is computed once.
export function customPlan(l: OfficeLayout): FloorPlan {
  const key = JSON.stringify(l);
  let plan = cache.get(key);
  if (!plan) {
    plan = build(l);
    cache.set(key, plan);
  }
  return plan;
}

// subtract removes b from a: up to four pieces of a around b.
function subtract(a: Rect, b: Rect): Rect[] {
  const x0 = Math.max(a.x, b.x), x1 = Math.min(a.x + a.w, b.x + b.w);
  const y0 = Math.max(a.y, b.y), y1 = Math.min(a.y + a.h, b.y + b.h);
  if (x0 >= x1 || y0 >= y1) return [a];
  const out: Rect[] = [];
  if (a.y < y0) out.push({ x: a.x, y: a.y, w: a.w, h: y0 - a.y });
  if (y1 < a.y + a.h) out.push({ x: a.x, y: y1, w: a.w, h: a.y + a.h - y1 });
  if (a.x < x0) out.push({ x: a.x, y: y0, w: x0 - a.x, h: y1 - y0 });
  if (x1 < a.x + a.w) out.push({ x: x1, y: y0, w: a.x + a.w - x1, h: y1 - y0 });
  return out;
}

function cut(rects: readonly Rect[], gaps: readonly Rect[]): Rect[] {
  let out = [...rects];
  for (const g of gaps) out = out.flatMap((r) => subtract(r, g));
  return out;
}

const inside = (p: Point, r: Rect, gx = 0, gy = 0): boolean =>
  p.x >= r.x - gx && p.x < r.x + r.w + gx && p.y >= r.y - gy && p.y < r.y + r.h + gy;

function build(l: OfficeLayout): FloorPlan {
  const W = l.columns * tile;
  const H = l.rows * tile;
  const full = (r: LayoutRoom): Rect => ({ x: r.x * tile, y: r.y * tile, w: r.w * tile, h: r.h * tile });
  const floorOf = (r: LayoutRoom): Rect => {
    const f = full(r);
    return { x: f.x, y: f.y + face, w: f.w, h: f.h - face - wall };
  };
  const byId = new Map(l.rooms.map((r) => [r.id as RoomId, r]));
  const F = (id: RoomId): Rect | undefined => {
    const r = byId.get(id);
    return r ? floorOf(r) : undefined;
  };
  const must = (id: RoomId): Rect => F(id) as Rect; // required rooms: the daemon checked they exist

  const rooms: Room[] = l.rooms.map((r) => {
    const info = roomInfo[r.id as RoomId];
    const f = full(r);
    return { id: r.id as RoomId, name: r.name || info.name, purpose: info.purpose, floor: info.floor, rect: floorOf(r), label: { x: f.x + 6, y: f.y + 2 } };
  });

  // Doorways: in the middle of the shared wall. Between rooms one above the
  // other, through the upper room's bottom wall and the lower room's back
  // wall; between rooms side by side, through the partition at floor level.
  const doorways: Rect[] = [];
  const doorOf = new Map<string, Rect>();
  for (const d of l.doors) {
    const a = byId.get(d.a), b = byId.get(d.b);
    if (!a || !b) continue;
    const [top, low] = a.y + a.h === b.y ? [a, b] : b.y + b.h === a.y ? [b, a] : [undefined, undefined];
    let rect: Rect | undefined;
    if (top && low) {
      const lo = Math.max(top.x, low.x) * tile + doorEdge, hi = Math.min(top.x + top.w, low.x + low.w) * tile - doorEdge;
      rect = { x: snap((lo + hi) / 2 - doorwayW / 2), y: (top.y + top.h) * tile - wall, w: doorwayW, h: wall + face };
    } else {
      const [left, right] = a.x + a.w === b.x ? [a, b] : [b, a];
      const lo = Math.max(left.y, right.y) * tile + face, hi = Math.min(left.y + left.h, right.y + right.h) * tile - wall;
      rect = { x: right.x * tile - wall / 2, y: snap((lo + hi) / 2 - doorwayW / 2), w: wall, h: doorwayW };
    }
    doorways.push(rect);
    doorOf.set(`${d.a}|${d.b}`, rect);
    doorOf.set(`${d.b}|${d.a}`, rect);
  }

  // Back walls, and walls seen from above: each room's bottom, partitions on
  // shared side edges, the outer walls at the grid's edges.
  const faces = cut(l.rooms.map((r) => ({ ...full(r), h: face })), doorways);
  const wallSet = new Map<string, Rect>();
  const addWall = (r: Rect): void => { wallSet.set(`${r.x},${r.y},${r.w},${r.h}`, r); };
  for (const r of l.rooms) {
    const f = full(r);
    addWall({ x: f.x, y: f.y + f.h - wall, w: f.w, h: wall });
    addWall(f.x === 0 ? { x: 0, y: f.y, w: wall, h: f.h } : { x: f.x - wall / 2, y: f.y, w: wall, h: f.h });
    addWall(f.x + f.w === W ? { x: W - wall, y: f.y, w: wall, h: f.h } : { x: f.x + f.w - wall / 2, y: f.y, w: wall, h: f.h });
  }
  const walls = cut([...wallSet.values()], doorways);

  // The Tech Lead's room: the whiteboard on the wall, the desk under it.
  const lead = must("lead");
  const leadCx = Math.round(lead.x + lead.w / 2);
  const whiteboard: Point = { x: leadCx - Math.floor(art.whiteboardSize.w / 2), y: lead.y - face + 6 };
  const leadDesk: Point = { x: leadCx - cell.w / 2, y: lead.y + 4 };
  const whiteboardSpots = [{ x: leadCx - 32, y: lead.y + 22 }, { x: leadCx + 44, y: lead.y + 22 }, { x: leadCx - 48, y: lead.y + 22 }];

  // The review room, when there is one: a screen on the wall, a table with seats.
  const review = F("review");
  let screen: Point | undefined, table: Rect | undefined;
  let seats: Point[] = [];
  if (review) {
    const cx = Math.round(review.x + review.w / 2);
    screen = { x: cx - art.wallScreenSize.w / 2, y: review.y - face + 8 };
    const tw = Math.min(88, snap(review.w - 34));
    table = { x: cx - Math.round(tw / 2), y: review.y + 22, w: tw, h: art.tableHeight };
    const n = Math.max(2, Math.min(4, Math.floor(tw / 22)));
    seats = Array.from({ length: n }, (_, i) => ({ x: (table as Rect).x + Math.round(((i + 0.5) * tw) / n), y: (table as Rect).y - 6 }));
  }

  // The kitchen, when there is one: the counter with the coffee machine.
  const kitchen = F("kitchen");
  const counter: Point | undefined = kitchen ? { x: kitchen.x + 8, y: kitchen.y - art.counterTop } : undefined;
  const coffee = counter && kitchen
    ? [{ x: counter.x + 7, y: kitchen.y + 26 }, { x: counter.x + 21, y: kitchen.y + 26 }, { x: counter.x + 14, y: kitchen.y + 40 }]
    : [];

  // The lobby: the front door at the right of its back wall, reception beside it.
  const lobby = must("lobby");
  const door: Point = { x: lobby.x + lobby.w - art.door.w - 8, y: lobby.y - art.door.h };
  const entrance: Point = { x: door.x + Math.floor(art.door.w / 2), y: lobby.y + 6 };

  const station = (kind: StationKind, cx: number, y: number, w: number, h: number): Station => {
    const furniture: Rect = { x: Math.round(cx - w / 2), y, w, h };
    return { kind, furniture, seat: { x: Math.round(cx), y: y - 6 }, sitY: y - 15, nameplate: { x: Math.round(cx), y: y + h + 7 } };
  };
  const stations: Partial<Record<StationKind, Station>> = {};
  const planning = F("planning");
  if (planning) {
    stations.architect = station("architect", planning.x + planning.w / 2, planning.y + 20, art.draftingSize.w, art.draftingSize.h);
  }
  const qa = F("qa");
  if (qa) stations.qa = station("qa", qa.x + qa.w / 2, qa.y + 22, Math.min(48, snap(qa.w - 40)), art.tableHeight);
  const recW = Math.min(36, Math.max(24, snap(door.x - lobby.x - 48)));
  stations.assistant = station("assistant", door.x - 12 - recW / 2, lobby.y + 24, recW, art.receptionHeight);

  // The user's office: a desk under the window; its first door is where the queue forms.
  const user = must("user");
  const userDesk: Point = { x: user.x + Math.round((user.w - cell.w) / 2) + 6, y: user.y + 2 };
  const userDoorPair = l.doors.find((d) => d.a === "user" || d.b === "user");
  const userDoor = (userDoorPair ? doorOf.get(`${userDoorPair.a}|${userDoorPair.b}`) : undefined) ?? { x: user.x, y: user.y + user.h, w: doorwayW, h: wall };
  const queueSign: Point = { x: Math.min(userDoor.x + userDoor.w + 2, W - 60), y: Math.max(userDoor.y - 12, 0) };

  // The workroom's desks: whole columns between the margins, rows from its back wall.
  const work = must("work");
  const columns = Math.max(1, Math.floor((work.w - 2 * margin) / cell.w));
  const rows = Math.max(1, Math.ceil(l.desks / columns));
  const deskX = work.x + Math.round((work.w - columns * cell.w) / 2 / 4) * 4;
  const desks: Point[] = [];
  for (let i = 0; i < l.desks; i++) desks.push({ x: deskX + (i % columns) * cell.w, y: work.y + corridor + Math.floor(i / columns) * cell.h });

  const cooler: Point = { x: work.x + work.w - wall - art.waterCooler.w - 2, y: work.y + work.h - art.waterCooler.h - 2 };
  const plants: Point[] = [
    { x: lead.x + wall + 2, y: lead.y + lead.h - art.plant.h - 2 },
    { x: work.x + wall + 2, y: work.y + work.h - art.plant.h - 2 },
    { x: lobby.x + lobby.w - wall - art.plant.w - 2, y: lobby.y + lobby.h - art.plant.h - 2 },
  ];
  const footprint = (p: Point, w: number, h: number, below: number): Rect => ({ x: p.x, y: p.y + h - below, w, h: below });
  const furniture: Rect[] = [
    ...(table ? [{ ...table, h: table.h - 2 }] : []),
    ...(counter && kitchen ? [{ x: counter.x, y: kitchen.y, w: art.counter.w, h: art.counter.h - art.counterTop }] : []),
    ...Object.values(stations).map((st) => ({ ...st.furniture, h: st.furniture.h - 2 })),
    { x: userDesk.x + deskFootprint.x, y: userDesk.y + deskFootprint.y, w: deskFootprint.w, h: deskFootprint.h },
    footprint(cooler, art.waterCooler.w, art.waterCooler.h, 8),
    ...plants.map((p) => footprint(p, art.plant.w, art.plant.h, 6)),
  ];

  // The queue at the user's door: out of it into the next room, straight on
  // to that room's far wall, then along it both ways. Spots on furniture,
  // desks or walls are left out.
  const deskRect = (o: Point): Rect => ({ x: o.x + deskFootprint.x, y: o.y + deskFootprint.y, w: deskFootprint.w, h: deskFootprint.h });
  const blockers = [...faces, ...walls, ...furniture, deskRect(leadDesk), ...desks.map(deskRect)];
  const free = (p: Point): boolean => p.x > 0 && p.y > 0 && p.x < W && p.y < H && !blockers.some((r) => inside(p, r, 6, 3));
  const queue: Point[] = [];
  const next = userDoorPair ? (userDoorPair.a === "user" ? userDoorPair.b : userDoorPair.a) as RoomId : undefined;
  const nr = next ? F(next) : undefined;
  if (nr) {
    const vertical = userDoor.w > userDoor.h;
    const line: Point[] = [];
    if (vertical) {
      const qx = userDoor.x + doorwayW / 2;
      const down = userDoor.y + userDoor.h / 2 < nr.y + nr.h / 2;
      const y0 = down ? nr.y + 10 : nr.y + nr.h - 10;
      const y1 = down ? nr.y + nr.h - 10 : nr.y + 10;
      for (let y = y0; down ? y <= y1 : y >= y1; y += down ? queueStep : -queueStep) line.push({ x: qx, y });
      const last = line[line.length - 1] ?? { x: qx, y: y0 };
      for (let x = qx - queueStep; x >= nr.x + 12; x -= queueStep) line.push({ x, y: last.y });
      for (let x = qx + queueStep; x <= nr.x + nr.w - 12; x += queueStep) line.push({ x, y: last.y });
    } else {
      const qy = userDoor.y + doorwayW / 2;
      const right = userDoor.x < nr.x + nr.w / 2;
      const x0 = right ? nr.x + 12 : nr.x + nr.w - 12;
      const x1 = right ? nr.x + nr.w - 12 : nr.x + 12;
      for (let x = x0; right ? x <= x1 : x >= x1; x += right ? queueStep : -queueStep) line.push({ x, y: qy });
      const last = line[line.length - 1] ?? { x: x0, y: qy };
      for (let y = qy + queueStep; y <= nr.y + nr.h - 10; y += queueStep) line.push({ x: last.x, y });
      for (let y = qy - queueStep; y >= nr.y + 10; y -= queueStep) line.push({ x: last.x, y });
    }
    for (const p of line) if (free(p) && queue.length < maxQueue) queue.push(p);
    // Furniture in the way: more places in that room, nearest the door first.
    if (queue.length < 8) {
      const from = { x: userDoor.x + userDoor.w / 2, y: userDoor.y + userDoor.h / 2 };
      const extra: Point[] = [];
      for (let y = nr.y + 10; y <= nr.y + nr.h - 10; y += queueStep) {
        for (let x = nr.x + 12; x <= nr.x + nr.w - 12; x += queueStep) {
          const p = { x: Math.round(x), y: Math.round(y) };
          if (free(p) && !queue.some((q) => Math.abs(q.x - p.x) < queueStep && Math.abs(q.y - p.y) < queueStep)) extra.push(p);
        }
      }
      extra.sort((p, q) => Math.hypot(p.x - from.x, p.y - from.y) - Math.hypot(q.x - from.x, q.y - from.y));
      for (const p of extra) if (queue.length < maxQueue) queue.push(p);
    }
  }

  // Windows along the back walls, around what hangs there.
  const windows: Point[] = [];
  const fill = (from: number, to: number, y: number): void => {
    const n = Math.floor((to - from + 8) / 40);
    if (n <= 0) return;
    const gap = Math.floor((to - from - n * art.windowMap.w) / (n + 1));
    for (let i = 0; i < n; i++) windows.push({ x: from + gap + i * (art.windowMap.w + gap), y });
  };
  const blueprints: Point | undefined = planning ? { x: Math.round(planning.x + planning.w / 2 - 13), y: planning.y - face + 10 } : undefined;
  const testBoard: Point | undefined = qa ? { x: Math.round(qa.x + qa.w / 2 - 14), y: qa.y - face + 10 } : undefined;
  for (const r of l.rooms) {
    const f = floorOf(r);
    const items: [number, number][] = [];
    if (r.id === "lead") items.push([whiteboard.x, art.whiteboardSize.w]);
    if (r.id === "review" && screen) items.push([screen.x, art.wallScreenSize.w]);
    if (r.id === "planning" && blueprints) items.push([blueprints.x, 26]);
    if (r.id === "qa" && testBoard) items.push([testBoard.x, 28]);
    if (r.id === "lobby") items.push([door.x, art.door.w]);
    if (r.id === "kitchen" && counter) items.push([counter.x, art.counter.w]);
    // doorways through this room's back wall
    for (const d of doorways) if (d.w > d.h && d.y + d.h === f.y && d.x >= f.x && d.x < f.x + f.w) items.push([d.x, d.w]);
    items.sort((a, b) => a[0] - b[0]);
    let from = f.x + wall + 2;
    for (const [x, w] of items) {
      fill(from, x - 4, f.y - face + 8);
      from = x + w + 4;
    }
    fill(from, f.x + f.w - wall - 2, f.y - face + 8);
  }

  return {
    columns, rows, stacked: false, custom: true, capacity: l.desks, w: W, h: H, rooms, faces, walls, doorways, windows, lead: leadDesk, desks,
    whiteboard, screen, table, counter, door, entrance, userDesk, userDoor, queueSign, blueprints, testBoard, stations, cooler, plants,
    furniture, spots: { whiteboard: whiteboardSpots, coffee, review: seats, queue },
  };
}

// deskCapacity mirrors internal/office.DeskCapacity, for the preview's warning.
export function deskCapacity(r: Pick<LayoutRoom, "w" | "h">): number {
  const cols = Math.floor((r.w * tile - 2 * margin) / cell.w);
  const rows = Math.floor((r.h * tile - face - wall - corridor - bottom) / cell.h);
  return cols > 0 && rows > 0 ? cols * rows : 0;
}
