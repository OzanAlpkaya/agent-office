// Paths through the office (ADR 0045). Pure: a walkability grid built from
// the floor plan, a breadth-first search over it and string pulling, so a
// route is a few straight segments that never cross a wall or furniture.
import { type FloorPlan, type Point, type Rect, deskFootprint } from "./floor";

// Cells of the walkability grid, in world pixels.
export const step = 4;
// How far a character's feet keep from walls and furniture: half its width
// sideways, a little front to back.
export const clearance = { x: 5, y: 2 } as const;

export interface WalkGrid {
  readonly nx: number;
  readonly ny: number;
  readonly free: Uint8Array; // 1 where feet may stand, by cell (x + y * nx)
}

const grids = new WeakMap<FloorPlan, WalkGrid>();

// blockers lists everything feet cannot stand on.
export function blockers(plan: FloorPlan): Rect[] {
  const desk = (o: Point): Rect => ({ x: o.x + deskFootprint.x, y: o.y + deskFootprint.y, w: deskFootprint.w, h: deskFootprint.h });
  return [...plan.faces, ...plan.walls, ...plan.furniture, desk(plan.lead), ...plan.desks.map(desk)];
}

export function walkGrid(plan: FloorPlan): WalkGrid {
  let g = grids.get(plan);
  if (g) return g;
  const nx = Math.ceil(plan.w / step);
  const ny = Math.ceil(plan.h / step);
  const free = new Uint8Array(nx * ny).fill(1);
  for (const r of blockers(plan)) {
    const x0 = r.x - clearance.x, x1 = r.x + r.w + clearance.x;
    const y0 = r.y - clearance.y, y1 = r.y + r.h + clearance.y;
    // every cell whose centre lies inside the grown rectangle
    for (let j = Math.max(0, Math.ceil((y0 - step / 2) / step)); j < ny && j * step + step / 2 < y1; j++) {
      for (let i = Math.max(0, Math.ceil((x0 - step / 2) / step)); i < nx && i * step + step / 2 < x1; i++) free[i + j * nx] = 0;
    }
  }
  g = { nx, ny, free };
  grids.set(plan, g);
  return g;
}

const cellOf = (p: Point): [number, number] => [Math.floor(p.x / step), Math.floor(p.y / step)];
const centre = (i: number, j: number): Point => ({ x: i * step + step / 2, y: j * step + step / 2 });

export function isFree(g: WalkGrid, p: Point): boolean {
  const [i, j] = cellOf(p);
  return i >= 0 && j >= 0 && i < g.nx && j < g.ny && g.free[i + j * g.nx] === 1;
}

// nearestFree is the free cell closest to p, searching outwards a few cells.
function nearestFree(g: WalkGrid, p: Point): number | undefined {
  const [ci, cj] = cellOf(p);
  let best: number | undefined;
  let bestD = Infinity;
  for (let r = 0; r <= 6 && best === undefined; r++) {
    for (let j = cj - r; j <= cj + r; j++) {
      for (let i = ci - r; i <= ci + r; i++) {
        if (Math.max(Math.abs(i - ci), Math.abs(j - cj)) !== r) continue;
        if (i < 0 || j < 0 || i >= g.nx || j >= g.ny || g.free[i + j * g.nx] !== 1) continue;
        const c = centre(i, j);
        const d = (c.x - p.x) ** 2 + (c.y - p.y) ** 2;
        if (d < bestD) {
          bestD = d;
          best = i + j * g.nx;
        }
      }
    }
  }
  return best;
}

// clear says whether a straight segment stays on free cells (sampled every pixel).
export function clear(g: WalkGrid, a: Point, b: Point): boolean {
  const n = Math.max(1, Math.ceil(Math.hypot(b.x - a.x, b.y - a.y)));
  for (let s = 0; s <= n; s++) {
    if (!isFree(g, { x: a.x + ((b.x - a.x) * s) / n, y: a.y + ((b.y - a.y) * s) / n })) return false;
  }
  return true;
}

const neighbours = [[1, 0], [-1, 0], [0, 1], [0, -1], [1, 1], [1, -1], [-1, 1], [-1, -1]] as const;

// route finds a walk from a to b: a, a few turning points, b. If a or b is
// not on a free cell (a seat), the walk starts or ends at the nearest free
// one. Without any route it returns the straight line, which the tests rule
// out for every place in every floor plan.
export function route(plan: FloorPlan, a: Point, b: Point): Point[] {
  const g = walkGrid(plan);
  const start = nearestFree(g, a);
  const goal = nearestFree(g, b);
  if (start === undefined || goal === undefined) return [a, b];
  const prev = new Int32Array(g.nx * g.ny).fill(-1);
  prev[start] = start;
  const queue = new Int32Array(g.nx * g.ny);
  let head = 0, tail = 0;
  queue[tail++] = start;
  while (head < tail) {
    const c: number = queue[head++];
    if (c === goal) break;
    const ci: number = c % g.nx;
    const cj: number = Math.floor(c / g.nx);
    for (const [di, dj] of neighbours) {
      const i: number = ci + di;
      const j: number = cj + dj;
      if (i < 0 || j < 0 || i >= g.nx || j >= g.ny) continue;
      const n = i + j * g.nx;
      if (prev[n] !== -1 || g.free[n] !== 1) continue;
      // diagonals only where both straight neighbours are free: no corner cutting
      if (di !== 0 && dj !== 0 && (g.free[ci + di + cj * g.nx] !== 1 || g.free[ci + (cj + dj) * g.nx] !== 1)) continue;
      prev[n] = c;
      queue[tail++] = n;
    }
  }
  if (prev[goal] === -1) return [a, b];
  const cells: Point[] = [];
  for (let c = goal; ; c = prev[c]) {
    cells.push(centre(c % g.nx, Math.floor(c / g.nx)));
    if (c === start) break;
  }
  cells.reverse();
  // String pulling: from each point, jump to the farthest one in plain sight.
  const pulled: Point[] = [cells[0]];
  for (let i = 0; i < cells.length - 1; ) {
    let j = cells.length - 1;
    while (j > i + 1 && !clear(g, cells[i], cells[j])) j--;
    pulled.push(cells[j]);
    i = j;
  }
  const out: Point[] = [a];
  for (const p of pulled) {
    const last = out[out.length - 1];
    if (p.x !== last.x || p.y !== last.y) out.push(p);
  }
  const last = out[out.length - 1];
  if (last.x !== b.x || last.y !== b.y) out.push(b);
  return out;
}

export function length(points: readonly Point[]): number {
  let n = 0;
  for (let i = 1; i < points.length; i++) n += Math.hypot(points[i].x - points[i - 1].x, points[i].y - points[i - 1].y);
  return n;
}

// along returns the point at distance d along a route, and the segment it is on.
export function along(points: readonly Point[], d: number): { at: Point; segment: number } {
  let left = d;
  for (let i = 1; i < points.length; i++) {
    const a = points[i - 1], b = points[i];
    const len = Math.hypot(b.x - a.x, b.y - a.y);
    if (left <= len && len > 0) {
      const t = left / len;
      return { at: { x: a.x + (b.x - a.x) * t, y: a.y + (b.y - a.y) * t }, segment: i - 1 };
    }
    left -= len;
  }
  return { at: points[points.length - 1], segment: Math.max(0, points.length - 2) };
}
