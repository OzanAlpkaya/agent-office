// The Pixi scene of the office (ADR 0024, ADR 0045). It draws an OfficeModel
// on a floor plan with rooms, animates poses and walks characters between
// their desk and the places their errands point to. It holds no state beyond
// what is on screen and has no actions except telling its owner which agent
// was clicked.
// The daemon's Content-Security-Policy has no unsafe-eval (ADR 0040); this pixi.js module
// replaces its generated shader and uniform code with eval-free versions.
import "pixi.js/unsafe-eval";
import { Application, Container, Graphics, Rectangle, Sprite, type Texture, TilingSprite } from "pixi.js";
import type { Agent, Kind, TaskStatus } from "../api";
import * as art from "./art";
import { armRects } from "./arms";
import { type Capacity, Director, type Errand, type Place } from "./errands";
import { type FloorKind, type FloorPlan, type Point, type Station, deskExit, roomAt, stationKinds } from "./floor";
import { type Grid, type Slot, fitGrid, gridFor, inCell, planOf, sameGrid, slotOrigin, worldSize } from "./layout";
import type { Note, OfficeModel, Seat } from "./model";
import { along, length, route } from "./paths";
import { type AvatarLook, avatarPalette, defaultLook, lookKey, sceneLook } from "./avatar";
import { type Look, type Pose, shirtOf } from "./pose";
import { TextureCache } from "./textures";

export interface Transform {
  scale: number; // CSS pixels per world pixel
  x: number; // CSS offset of the world's origin inside the canvas
  y: number;
  width: number; // canvas size in CSS pixels
  height: number;
  grid: Grid;
}

// FrameStats describes the scene's own work: how often it ticked, how often it
// had something to draw, and what drawing cost (tick plus render).
export interface FrameStats {
  ticks: number;
  renders: number;
  p95Ms: number;
  maxMs: number;
  left: number; // characters that walked out since the scene started (exited agents)
  walking: number; // characters walking right now
  trips: number; // walks started since the scene started
  busyFrames: number; // frames drawn while at least busyWalkers characters walked
  busyP95Ms: number;
  busyMaxMs: number;
  // Heads of characters away from their desk, in CSS pixels inside the canvas.
  figures: Record<string, { x: number; y: number }>;
}

// The budget's "busy office": this many characters walking at once (ADR 0045).
export const busyWalkers = 10;

// Where a character is, for the nameplates and the UI tests.
export type Whereabouts = Place | "walking";

export interface SceneOptions {
  reducedMotion: boolean;
  onOpen: (agentId: string) => void;
  onHover: (agentId: string | undefined) => void;
  onPlaces?: (places: Record<string, Whereabouts>) => void;
  // The office changed size by itself (a held row of desks went once the last leaver left).
  onTransform?: (t: Transform) => void;
}

const noteColors: Record<TaskStatus, art.Swatch> = {
  todo: { base: 0xf2d16b, shade: 0xc9a63c },
  in_progress: { base: 0x8ab4f8, shade: 0x5a8de0 },
  review: { base: 0xc49bff, shade: 0x9b6dde },
  done: { base: 0x8fd694, shade: 0x5cb860 },
  blocked: { base: 0xf28b82, shade: 0xd05a50 },
};

const background = 0x0f1115;

// Walking: a steady pace, but no trip shorter or longer than these.
const walkSpeed = 64; // world pixels per second
const minTrip = 0.6;
const maxTrip = 4.5;
const stride = 5; // pixels per step
const fadeDistance = 12; // characters fade in and out over this much of the door's way
const maxWalkers = 40; // beyond this, characters change places without walking

const floorTextures: Record<FloorKind, [string, art.PixelMap, art.Palette]> = {
  carpet: ["floor-carpet", art.carpetTile, art.carpetPalette],
  tile: ["floor-tile", art.tileFloor, art.tilePalette],
  kitchen: ["floor-kitchen", art.tileFloor, art.kitchenPalette],
  wood: ["floor-wood", art.floorTile, art.floorPalette],
  blue: ["floor-blue", art.carpetTile, art.planningPalette],
  lab: ["floor-lab", art.tileFloor, art.labPalette],
  rug: ["floor-rug", art.carpetTile, art.userPalette],
};

export class OfficeScene {
  private readonly textures = new TextureCache();
  private readonly world = new Container();
  private readonly rooms = new Container(); // floors, walls and what hangs on them
  private readonly notes = new Container();
  private readonly sorted = new Container(); // desks, furniture and characters, by depth
  private furniture: Sprite[] = []; // furniture in the sorted layer, rebuilt with the floor plan
  private readonly flying = new Container();
  private readonly nodes: DeskNode[] = [];
  private userNode?: DeskNode; // the user's own desk (Phase 18): they sit there while a device has the UI open
  private userPresent = false;
  private userSeat?: Seat;
  private readonly envelopes: { sprite: Sprite; from: Point; to: Point; start: number }[] = [];
  private readonly chars = new Map<string, Character>();
  private readonly director = new Director();
  private left = 0;
  private trips = 0;
  private grid: Grid = gridFor(0, 4);
  private plan: FloorPlan = planOf(this.grid);
  private model?: OfficeModel;
  private hovered?: string;
  private size = { w: 1, h: 1 };
  private shownDesks = 0; // desks the grid was laid out for
  private destroyed = false;
  private dirty = true;
  private notesKey = "";
  private placesKey = "";
  private ticks = 0;
  private renders = 0;
  private readonly costs: number[] = []; // ms of the last rendered frames
  private readonly busyCosts: number[] = []; // ms of the last frames drawn while the office was busy
  private busyFrames = 0;

  private constructor(private readonly app: Application, private readonly opts: SceneOptions) {
    this.sorted.sortableChildren = true;
    this.world.addChild(this.rooms, this.sorted, this.flying);
    app.stage.addChild(this.world);
    // Render only frames where something changed: an idle office then costs
    // a few cheap ticks per second instead of 30 full renders.
    app.ticker.remove(app.render, app);
    app.ticker.maxFPS = 30;
    app.ticker.add(() => this.frame());
    // Render once now: Pixi hit-tests from the last rendered root, so a click
    // before the first tick would otherwise find nothing.
    app.render();
  }

  static async create(host: HTMLElement, opts: SceneOptions): Promise<OfficeScene> {
    const app = new Application();
    await app.init({
      preference: ["webgl", "canvas"],
      background,
      antialias: false,
      roundPixels: true,
      resolution: window.devicePixelRatio || 1,
      autoDensity: true,
      width: Math.max(1, host.clientWidth),
      height: Math.max(1, host.clientHeight),
    });
    host.appendChild(app.canvas);
    return new OfficeScene(app, opts);
  }

  get renderer(): string {
    return this.app.renderer.name;
  }

  stats(): FrameStats {
    const p95 = (xs: number[]): [number, number] => {
      const s = xs.slice().sort((a, b) => a - b);
      return s.length > 0 ? [s[Math.floor(s.length * 0.95)], s[s.length - 1]] : [0, 0];
    };
    const [p95Ms, maxMs] = p95(this.costs);
    const [busyP95Ms, busyMaxMs] = p95(this.busyCosts);
    const figures: FrameStats["figures"] = {};
    const sc = this.world.scale.x;
    for (const c of this.chars.values()) {
      const r = c.figure?.root;
      if (!r || c.mode === "seated") continue;
      figures[c.id] = { x: this.world.x + (r.x + art.body.w / 2) * sc, y: this.world.y + (r.y + 7) * sc };
    }
    return {
      ticks: this.ticks, renders: this.renders, left: this.left, p95Ms, maxMs, walking: this.walkingCount(), trips: this.trips,
      busyFrames: this.busyFrames, busyP95Ms, busyMaxMs, figures,
    };
  }

  // leaving says whether a character without a seat is still in the office.
  private leaving(): boolean {
    for (const c of this.chars.values()) if (!c.seat) return true;
    return false;
  }

  private walkingCount(): number {
    let n = 0;
    for (const c of this.chars.values()) if (c.mode === "walking") n++;
    return n;
  }

  private frame(): void {
    const start = performance.now();
    this.ticks++;
    const changed = this.tick(start / 1000);
    if (!changed && !this.dirty) return;
    this.draw(start);
  }

  // draw renders the stage and records what it cost since start.
  private draw(start: number): void {
    this.dirty = false;
    const walking = this.walkingCount();
    this.app.render();
    this.renders++;
    const cost = performance.now() - start;
    this.costs.push(cost);
    if (this.costs.length > 300) this.costs.shift();
    if (walking >= busyWalkers) {
      this.busyFrames++;
      this.busyCosts.push(cost);
      if (this.busyCosts.length > 300) this.busyCosts.shift();
    }
  }

  destroy(): void {
    if (this.destroyed) return;
    this.destroyed = true;
    this.app.destroy({ removeView: true }, { children: true });
    this.textures.destroy();
  }

  // resize fits the office into a pane of w×h CSS pixels (see fitGrid).
  resize(w: number, h: number): Transform {
    this.size = { w: Math.max(1, w), h: Math.max(1, h) };
    const t = this.layout();
    this.draw(performance.now());
    return t;
  }

  // layout picks the grid and a whole number of device pixels per world
  // pixel, so pixels stay square and sharp, and rebuilds what depends on the
  // grid when it changed.
  private layout(): Transform {
    // Read the ratio each time: browser zoom and moving the window to another
    // screen change it.
    const dpr = window.devicePixelRatio || 1;
    const { w, h } = this.size;
    // While someone walks out, the office keeps its size: a row of desks
    // vanishing under their feet would cut the walk short.
    const want = this.model?.desks ?? 0;
    this.shownDesks = this.leaving() ? Math.max(want, this.shownDesks) : want;
    const { grid, k } = fitGrid(w, h, dpr, this.shownDesks, this.model?.layout);
    if (!sameGrid(grid, this.grid) || this.nodes.length === 0) this.setGrid(grid);
    const { w: W, h: H } = worldSize(grid);
    const scale = k / dpr;
    const width = Math.max(w, Math.ceil(W * scale));
    const height = Math.max(h, Math.ceil(H * scale));
    const x = Math.round(((width - W * scale) / 2) * dpr) / dpr;
    const y = 0; // from the top: a shorter office leaves no band above it (Phase 18 review)
    this.app.renderer.resize(width, height, dpr);
    this.world.scale.set(scale);
    this.world.position.set(x, y);
    this.dirty = true;
    return { scale, x, y, width, height, grid };
  }

  // update shows a new model. It returns a new transform when the grid
  // changed (the office gained or lost a row of desks).
  update(model: OfficeModel): Transform | undefined {
    const before = this.grid;
    const first = this.model === undefined;
    const now = performance.now() / 1000;
    // Characters whose agent lost its seat (exited or removed) stand up where
    // they sat, before the grid may change under them.
    const seats = new Map(model.seats.map((s) => [s.agent.id, s]));
    for (const c of this.chars.values()) {
      if (seats.has(c.id) || c.seat === undefined) continue;
      if (c.mode === "seated") this.standUp(c, slotOrigin(c.seat.slot, this.grid));
      c.seat = undefined;
    }
    // Characters that are still (or again) seated get their seat before the
    // layout, so a grid change knows who is coming back (a resumed agent).
    for (const s of model.seats) {
      const c = this.chars.get(s.agent.id);
      if (c && c.seat === undefined) c.seat = s;
    }
    this.model = model;
    const t = this.layout();
    // New characters: at their desk on the first view, else in through the door.
    for (const s of model.seats) {
      let c = this.chars.get(s.agent.id);
      if (!c) {
        c = new Character(s.agent.id, s, this.figureLook(s));
        if (s.slot.kind === "station") {
          // no desk to sit at: placed at its station by its first errand
          c.mode = "standing";
          c.goal = "";
          c.pos = this.plan.stations[s.slot.station]?.seat ?? this.plan.entrance;
        }
        this.chars.set(c.id, c);
        if (!first && !this.opts.reducedMotion) {
          c.mode = "standing";
          c.goal = "enter"; // it is at the door; its errand sends it to its desk
          c.pos = this.plan.entrance;
          c.fadeIn = true;
          this.ensureFigure(c);
        }
      } else {
        const moved = c.seat !== undefined && slotKey(c.seat.slot) !== slotKey(s.slot);
        if (moved && c.mode === "seated" && s.slot.kind === "station" && c.seat) {
          // from a desk to its station (a role's own place came free)
          this.standUp(c, slotOrigin(c.seat.slot, this.grid));
          c.goal = "";
        }
        if (c.seat?.agent.kind !== s.agent.kind || c.seat.agent.role !== s.agent.role) {
          c.look = this.figureLook(s);
          c.figure?.setLook(c.look);
        }
        // A character walking to a desk that is no longer its own picks a new way.
        if (moved && c.mode !== "seated" && c.goal === "desk") c.goal = "";
      }
      c.seat = s;
    }
    const bySlot = new Map(model.seats.map((s) => [slotKey(s.slot), s]));
    for (const n of this.nodes) n.setSeat(bySlot.get(slotKey(n.slot)));
    this.director.observe([
      ...model.seats.map((s) => ({
        id: s.agent.id, present: true, pose: s.pose, taskStatus: model.taskStatus[s.agent.id], phase: s.look.phase,
        waitingSince: model.waitingSince[s.agent.id],
      })),
      ...model.gone.map((id) => ({ id, present: false, taskStatus: model.taskStatus[id], phase: 0 })),
    ], now);
    if (first) this.errands(now, true);
    this.syncDesks();
    const notes = model.goal?.notes ?? [];
    const notesKey = `${model.goal !== undefined}|${notes.map((n) => `${n.id}:${n.status}`).join(",")}`;
    if (notesKey !== this.notesKey) {
      this.notesKey = notesKey;
      this.drawNotes(notes);
    }
    this.dirty = true;
    if (this.hovered && !this.chars.has(this.hovered)) this.hover(undefined);
    this.publishPlaces();
    // Draw now, not on the next tick: Pixi hit-tests against the transforms
    // of the last render, so a click right after a change must see it.
    this.draw(performance.now());
    return sameGrid(t.grid, before) ? undefined : t;
  }

  // setPresence seats the user's character at their desk, in their look, or
  // empties it (Phase 18).
  setPresence(present: boolean, look: AvatarLook = defaultLook): void {
    const seat = present ? userSeat(look) : undefined;
    if (this.userPresent === present && this.userSeat?.paletteKey === seat?.paletteKey) return;
    this.userPresent = present;
    this.userSeat = seat;
    this.userNode?.setSeat(seat);
    this.dirty = true;
  }

  // sendEnvelope flies a letter from one agent to another, wherever they
  // are; a missing sender (agent-office itself) sends from the front door.
  sendEnvelope(fromAgent: string | undefined, toAgent: string): void {
    if (this.opts.reducedMotion || this.envelopes.length >= maxEnvelopes) return;
    const to = this.agentPoint(toAgent);
    if (!to) return;
    const door = { x: this.plan.door.x + art.door.w / 2 - 4, y: this.plan.door.y + 8 };
    const from = (fromAgent !== undefined ? this.agentPoint(fromAgent) : undefined) ?? door;
    const sprite = new Sprite(this.textures.get("envelope", art.envelope, art.basePalette));
    this.flying.addChild(sprite);
    this.envelopes.push({ sprite, from, to, start: performance.now() / 1000 });
  }

  // agentPoint is where an envelope to or from an agent starts or lands: its
  // head, at its desk or wherever it is.
  private agentPoint(agentId: string): Point | undefined {
    const c = this.chars.get(agentId);
    if (!c) return undefined;
    if (c.mode === "seated" && c.seat) {
      const o = slotOrigin(c.seat.slot, this.grid);
      return { x: o.x + inCell.person.x + 4, y: o.y + inCell.person.y };
    }
    return { x: c.pos.x - 4, y: c.pos.y - 22 };
  }

  hover(agentId: string | undefined): void {
    if (this.hovered === agentId) return;
    this.hovered = agentId;
    for (const n of this.nodes) n.setHover(n.agentId !== undefined && n.agentId === agentId);
    for (const c of this.chars.values()) c.figure?.setHover(c.id === agentId);
    this.dirty = true;
    this.opts.onHover(agentId);
  }

  // Called by desks and figures.
  open(agentId: string): void {
    this.opts.onOpen(agentId);
  }

  private setGrid(grid: Grid): void {
    const sameColumns = grid.columns === this.grid.columns && planOf(grid).custom === this.plan.custom &&
      (!grid.layout || planOf(grid) === this.plan) && this.nodes.length > 0;
    this.grid = grid;
    this.plan = planOf(grid);
    // A custom layout has its own number of desks; the default plan a full grid.
    const want = 1 + (grid.layout ? this.plan.desks.length : grid.rows * grid.columns);
    while (this.nodes.length < want) {
      const i = this.nodes.length;
      const slot: Slot = i === 0 ? { kind: "lead" } : { kind: "desk", index: i - 1 };
      const node = new DeskNode(this, this.textures, slot, this.opts.reducedMotion);
      this.nodes.push(node);
      this.sorted.addChild(node.root);
    }
    while (this.nodes.length > want) this.nodes.pop()?.root.destroy({ children: true });
    for (const n of this.nodes) n.place(grid);
    if (!this.userNode) {
      // Drawn still: the user is there whenever the office is looked at, and
      // an office where no agent moves must not redraw (ADR 0024).
      this.userNode = new DeskNode(this, this.textures, { kind: "user" }, true, false);
      this.sorted.addChild(this.userNode.root);
      this.userNode.setSeat(this.userSeat);
    }
    this.userNode.place(grid);
    this.buildRooms();
    // With the same columns, rooms and desks stay where they were and only
    // rows come or go: walkers find a new way from where they are. Otherwise
    // positions belong to the old plan, and everyone away from their desk is
    // where they were going, at once.
    const now = performance.now() / 1000;
    for (const c of [...this.chars.values()]) {
      if (c.mode === "seated") continue;
      if (sameColumns && c.mode === "walking") this.walkFrom(c, c.pos, now);
      else if (sameColumns) continue;
      else {
        // a walk-in, or a leaver whose agent came back (resumed), sits down
        if (c.goal === "enter" || (c.goal === "out" && c.seat)) c.goal = "desk";
        c.pos = this.target(c, c.goal, c.errand);
        this.arrive(c);
      }
    }
  }

  // buildRooms draws the floor plan: floors, back walls, partitions and the
  // furniture that is not a desk.
  private buildRooms(): void {
    const t = this.textures;
    const p = this.plan;
    this.rooms.removeChildren().forEach((c) => {
      if (c !== this.notes) c.destroy();
    });
    for (const f of this.furniture) f.destroy();
    this.furniture = [];
    const put = (layer: Container, tex: Texture, x: number, y: number): Sprite => {
      const s = new Sprite(tex);
      s.position.set(x, y);
      layer.addChild(s);
      return s;
    };
    const tiles = (kind: FloorKind, x: number, y: number, w: number, h: number): void => {
      const [key, map, palette] = floorTextures[kind];
      const s = new TilingSprite({ texture: t.get(key, map, palette), width: w, height: h });
      s.position.set(x, y);
      s.tilePosition.set(-x, -y); // the pattern stays put across rooms
      this.rooms.addChild(s);
    };
    for (const r of p.rooms) tiles(r.floor, r.rect.x, r.rect.y, r.rect.w, r.rect.h);
    // Doorways show the floor of the room they lead into.
    for (const d of p.doorways) tiles(roomAt(p, { x: d.x + 1, y: d.y + d.h + 1 })?.floor ?? "wood", d.x, d.y, d.w, d.h);
    const wallTex = t.get("wall", art.wallTile, art.wallPalette);
    for (const f of p.faces) {
      const s = new TilingSprite({ texture: wallTex, width: f.w, height: f.h });
      s.position.set(f.x, f.y);
      s.tilePosition.set(-f.x, 0);
      this.rooms.addChild(s);
    }
    const g = new Graphics();
    for (const w of p.walls) {
      g.rect(w.x, w.y, w.w, w.h).fill(art.wallTopColor);
      if (w.w > w.h) g.rect(w.x, w.y + w.h - 1, w.w, 1).fill(art.wallEdgeColor);
      else g.rect(w.x + w.w - 1, w.y, 1, w.h).fill(art.wallEdgeColor);
    }
    // Door frames where a doorway cuts through a back wall.
    for (const d of p.doorways) {
      if (d.h <= 4) continue;
      g.rect(d.x - 1, d.y, 1, d.h).fill(art.wallEdgeColor);
      g.rect(d.x + d.w, d.y, 1, d.h).fill(art.wallEdgeColor);
    }
    this.rooms.addChild(g);
    const win = t.get("window", art.windowMap, art.basePalette);
    for (const w of p.windows) put(this.rooms, win, w.x, w.y);
    put(this.rooms, t.get("whiteboard", art.whiteboardMap, art.basePalette), p.whiteboard.x, p.whiteboard.y);
    this.notes.position.set(p.whiteboard.x + art.noteGrid.x, p.whiteboard.y + art.noteGrid.y);
    this.rooms.addChild(this.notes);
    // A custom layout may lack the review room, the planning room and the QA lab (ADR 0057).
    if (p.screen) put(this.rooms, t.get("wall-screen", art.wallScreen, art.wallScreenPalette), p.screen.x, p.screen.y);
    put(this.rooms, t.get("door", art.door, art.basePalette), p.door.x, p.door.y);
    if (p.blueprints) put(this.rooms, t.get("wall-blueprint", art.wallBlueprint, art.blueprintPalette), p.blueprints.x, p.blueprints.y);
    if (p.testBoard) put(this.rooms, t.get("test-board", art.testBoard, art.basePalette), p.testBoard.x, p.testBoard.y);

    // Furniture, depth-sorted with desks and characters by its front edge.
    const fur = (tex: Texture, x: number, y: number, z: number): void => {
      const s = new Sprite(tex);
      s.position.set(x, y);
      s.zIndex = z;
      this.sorted.addChild(s);
      this.furniture.push(s);
    };
    const chair = t.get("chair", art.chair, art.basePalette);
    const table = p.table;
    if (table) {
      for (const s of p.spots.review) fur(chair, s.x - 8, table.y - 12, table.y - 2);
      fur(t.get(`table-${table.w}`, () => art.table(table.w), art.basePalette), table.x, table.y, table.y + table.h);
    }
    if (p.counter) fur(t.get("counter", art.counter, art.basePalette), p.counter.x, p.counter.y, p.counter.y + art.counter.h);
    fur(t.get("cooler", art.waterCooler, art.basePalette), p.cooler.x, p.cooler.y, p.cooler.y + art.waterCooler.h);
    const plant = t.get("plant", art.plant, art.basePalette);
    for (const pl of p.plants) fur(plant, pl.x, pl.y, pl.y + art.plant.h);
    // Stations (ADR 0045): their agents sit behind the furniture, drawn by figures.
    for (const k of stationKinds) {
      const st = p.stations[k];
      if (!st) continue;
      const f = st.furniture;
      fur(chair, st.seat.x - 8, f.y - 12, f.y - 2);
      if (k === "architect") fur(t.get("drafting", art.draftingTable, art.blueprintPalette), f.x, f.y, f.y + f.h);
      if (k === "qa") {
        fur(t.get(`bench-${f.w}`, () => art.bench(f.w), art.benchPalette), f.x, f.y, f.y + f.h);
        fur(t.get("test-rig", art.testRig, art.basePalette), f.x + f.w - art.testRig.w - 2, f.y - 6, f.y + f.h + 1);
      }
      if (k === "assistant") fur(t.get(`reception-${f.w}`, () => art.reception(f.w), art.basePalette), f.x, f.y, f.y + f.h);
    }
    // The user's own desk is a desk node (setPresence): empty, or with them at it.
  }

  private drawNotes(notes: readonly Note[]): void {
    this.notes.removeChildren().forEach((c) => c.destroy());
    if (this.model?.goal) {
      // The goal's title as a scribbled marker line: dashes of varying length.
      const line = new Graphics();
      const tl = art.titleLine;
      for (let x = 0, i = 0; x < tl.w; i++) {
        const len = 3 + ((i * 7) % 5);
        line.rect(tl.x - art.noteGrid.x + x, tl.y - art.noteGrid.y, Math.min(len, tl.w - x), 1).fill(0x4a5060);
        x += len + 2;
      }
      this.notes.addChild(line);
    }
    const g = art.noteGrid;
    notes.forEach((n, i) => {
      if (i >= g.cols * g.rows) return;
      const c = noteColors[n.status];
      const s = new Sprite(this.textures.get(`note-${n.status}`, art.noteMap, { u: c.base, U: c.shade }));
      s.position.set((i % g.cols) * g.dx, Math.floor(i / g.cols) * g.dy);
      this.notes.addChild(s);
    });
  }

  // ------------------------------------------------------------ characters

  private figureLook(s: Seat): FigureLook {
    const l = s.look;
    const palette = art.personPalette(art.skins[l.skin], art.hairColors[l.hair], shirtOf(s.agent));
    const key = `${l.skin}-${l.hair}-${s.agent.kind}-${s.agent.role}`;
    const t = this.textures;
    return {
      look: l, kind: s.agent.kind, palette,
      roll: t.get("blueprint-roll", art.blueprintRoll, art.blueprintPalette),
      note: t.get("note", art.note, art.basePalette),
      front: t.get(`body-${key}`, art.body, palette),
      back: t.get(`back-${key}`, art.bodyBack, palette),
      hair: t.get(`hair-${l.style}-${key}`, art.hair[l.style], palette),
      legs: art.legs.map((m, i) => t.get(`legs-${i}`, m, palette)),
      mug: t.get("mug", art.mug, art.basePalette),
      magnifier: t.get("magnifier", art.magnifier, art.basePalette),
    };
  }

  private ensureFigure(c: Character): Figure {
    if (!c.figure) {
      c.figure = new Figure(this, c.id, c.look);
      c.figure.setHover(this.hovered === c.id);
      this.sorted.addChild(c.figure.root);
    }
    return c.figure;
  }

  // dropFigure removes a character's figure. A destroyed figure never sends
  // pointerout, so a hover the pointer gave it ends here.
  private dropFigure(c: Character): void {
    if (!c.figure) return;
    if (c.figure.pointerInside && this.hovered === c.id) this.hover(undefined);
    c.figure.root.destroy({ children: true });
    c.figure = undefined;
  }

  // standUp takes a character out of its chair into the aisle next to its desk.
  private standUp(c: Character, origin: Point): void {
    this.dirty = true;
    c.mode = "standing";
    c.place = undefined;
    c.pos = { x: origin.x + deskExit.x, y: origin.y + deskExit.y };
    this.ensureFigure(c);
  }

  // errands moves everyone whose errand changed; instant places them at once.
  private errands(now: number, instant: boolean): void {
    const p = this.plan;
    const capacity: Capacity = {
      whiteboard: p.spots.whiteboard.length, coffee: p.spots.coffee.length, review: p.spots.review.length, queue: p.spots.queue.length,
    };
    const errands = this.director.errands(now, capacity);
    for (const c of [...this.chars.values()]) {
      const e = errands.get(c.id);
      const goal = e === undefined || (e.place === "desk" && !c.seat) ? "out" : errandKey(e);
      if (goal === c.goal) continue;
      this.go(c, goal, e, now, instant);
    }
  }

  private target(c: Character, goal: string, e: Errand | undefined): Point {
    const p = this.plan;
    if (goal === "out" || !e) return p.entrance;
    if (e.place === "desk") {
      const st = stationOf(c);
      const station = st ? p.stations[st] : undefined;
      if (station) return station.seat;
      const o = c.seat ? slotOrigin(c.seat.slot, this.grid) : p.lead;
      return { x: o.x + deskExit.x, y: o.y + deskExit.y };
    }
    return p.spots[e.place][e.spot] ?? p.entrance;
  }

  private go(c: Character, goal: string, e: Errand | undefined, now: number, instant: boolean): void {
    c.goal = goal;
    c.errand = e;
    if (c.mode === "seated") {
      if (goal === "desk") return;
      if (!c.seat) return;
      this.standUp(c, slotOrigin(c.seat.slot, this.grid));
    }
    if (instant || this.opts.reducedMotion || this.walkingCount() >= maxWalkers) {
      c.pos = this.target(c, goal, e);
      this.arrive(c);
      return;
    }
    if (this.walkFrom(c, c.pos, now)) this.trips++;
  }

  // walkFrom starts (or restarts) a character's walk from p to its goal; it
  // returns false when it was already there.
  private walkFrom(c: Character, p: Point, now: number): boolean {
    const to = this.target(c, c.goal, c.errand);
    const path = route(this.plan, p, to);
    const total = length(path);
    if (total < 1) {
      c.pos = to;
      this.arrive(c);
      return false;
    }
    c.route = path;
    c.total = total;
    c.duration = Math.min(maxTrip, Math.max(minTrip, total / walkSpeed));
    c.start = now;
    c.mode = "walking";
    c.place = undefined;
    this.ensureFigure(c).root.alpha = c.fadeIn ? 0 : 1;
    this.syncDesks();
    return true;
  }

  // arrive ends a trip: the character sits at its desk, stands at its spot,
  // sits at the review table, or leaves.
  private arrive(c: Character): void {
    c.route = undefined;
    c.fadeIn = false;
    this.dirty = true; // a figure appears, moves or goes; the desk may change too
    const goal = c.goal;
    if (goal === "out") {
      this.dropFigure(c);
      this.chars.delete(c.id);
      this.left++;
      this.syncDesks();
      return;
    }
    if (goal === "desk" || goal === "enter" || c.errand === undefined || c.errand.place === "desk") {
      if (!c.seat) {
        // its desk is gone: leave instead
        c.goal = "out";
        this.arrive(c);
        return;
      }
      const st = stationOf(c);
      if (st) {
        // home is a station: sitting behind its furniture, drawn as a figure
        c.pos = this.plan.stations[st]?.seat ?? this.plan.entrance;
        c.mode = "standing";
        c.place = "desk";
        this.ensureFigure(c).root.alpha = 1;
        this.syncDesks();
        return;
      }
      c.mode = "seated";
      c.place = "desk";
      this.dropFigure(c);
      this.syncDesks();
      return;
    }
    c.pos = this.target(c, goal, c.errand);
    c.mode = "standing";
    c.place = c.errand.place;
    const f = this.ensureFigure(c);
    f.root.alpha = 1;
    this.syncDesks();
  }

  // syncDesks shows a seated character at its desk and an empty chair when
  // it is away.
  private syncDesks(): void {
    for (const n of this.nodes) {
      const c = n.agentId !== undefined ? this.chars.get(n.agentId) : undefined;
      n.setAway(c !== undefined && c.mode !== "seated");
    }
  }

  private publishPlaces(): void {
    const places: Record<string, Whereabouts> = {};
    for (const c of this.chars.values()) {
      if (!c.seat) continue;
      places[c.id] = c.mode === "walking" ? "walking" : c.mode === "seated" ? "desk" : (c.place ?? "walking");
    }
    const key = JSON.stringify(places);
    if (key === this.placesKey) return;
    this.placesKey = key;
    this.opts.onPlaces?.(places);
  }

  // tick advances the animations; it returns whether anything changed.
  private tick(now: number): boolean {
    const t = this.opts.reducedMotion ? 0 : now;
    if (this.model) this.errands(now, false);
    let changed = this.envelopes.length > 0;
    for (const n of this.nodes) changed = n.tick(t) || changed;
    if (this.userNode) changed = this.userNode.tick(t) || changed;
    for (const c of [...this.chars.values()]) {
      if (c.mode === "walking" && c.route) {
        changed = true;
        const p = Math.min(1, (now - c.start) / c.duration);
        const d = p * c.total;
        const { at, segment } = along(c.route, d);
        c.pos = at;
        const a = c.route[segment], b = c.route[segment + 1] ?? a;
        const away = b.y - a.y < 0 && Math.abs(b.y - a.y) > Math.abs(b.x - a.x);
        const step = Math.floor(d / stride) % 2;
        const fig = this.ensureFigure(c);
        fig.walk(at, step, away);
        if (c.goal === "out") fig.root.alpha = Math.max(0, Math.min(1, (c.total - d) / fadeDistance));
        else if (c.fadeIn) {
          fig.root.alpha = Math.max(0, Math.min(1, d / fadeDistance));
          if (d >= fadeDistance) c.fadeIn = false; // a re-route must not fade it in again
        }
        if (p >= 1) {
          c.pos = c.route[c.route.length - 1];
          this.arrive(c);
        }
      } else if (c.mode === "standing" && c.figure) {
        const st = c.place === "desk" ? stationOf(c) : undefined;
        changed = c.figure.stand(c.pos, c.place, this.plan, t + c.look.look.phase * 10,
          st ? this.plan.stations[st] : undefined, c.seat?.pose) || changed;
      }
    }
    for (let i = this.envelopes.length - 1; i >= 0; i--) {
      const e = this.envelopes[i];
      const p = Math.min(1, (now - e.start) / envelopeSeconds);
      const arc = Math.sin(p * Math.PI) * 18;
      e.sprite.position.set(
        Math.round(e.from.x + (e.to.x - e.from.x) * p),
        Math.round(e.from.y + (e.to.y - e.from.y) * p - arc),
      );
      e.sprite.alpha = p > 0.85 ? (1 - p) / 0.15 : 1;
      if (p >= 1) {
        e.sprite.destroy();
        this.envelopes.splice(i, 1);
      }
    }
    // The last leaver is gone: the office may lose the rows it held for them.
    if (this.model && this.shownDesks > this.model.desks && !this.leaving()) {
      const before = this.grid;
      const t = this.layout();
      if (!sameGrid(t.grid, before)) this.opts.onTransform?.(t);
      changed = true;
    }
    this.publishPlaces();
    return changed;
  }
}

const envelopeSeconds = 1.4;
// Envelopes in flight at most; more are skipped (e.g. many messages while the
// tab was in the background).
const maxEnvelopes = 20;

// The user's character (Phase 18): its own look and shirt, at work while present.
const userAgent = { id: "you", name: "You", kind: "worker", role: "you", state: "working" } as Agent;
const userSeat = (l: AvatarLook): Seat => ({
  slot: { kind: "user" }, agent: userAgent, pose: "typing", look: sceneLook(l), palette: avatarPalette(l), paletteKey: lookKey(l),
});

const slotKey = (s: Slot): string =>
  s.kind === "lead" ? "lead" : s.kind === "user" ? "user" : s.kind === "station" ? `station-${s.station}` : `desk-${s.index}`;
// stationOf is the station a character's home is, if it has one.
const stationOf = (c: { seat?: Seat }): Station["kind"] | undefined =>
  c.seat?.slot.kind === "station" ? c.seat.slot.station : undefined;
const errandKey = (e: Errand): string => (e.place === "desk" ? "desk" : `${e.place}:${e.spot}`);

interface FigureLook {
  look: Look;
  kind: Kind;
  palette: art.Palette;
  roll: Texture; // the architect's plan, carried to the user's door
  note: Texture;
  front: Texture;
  back: Texture;
  hair: Texture;
  legs: Texture[];
  mug: Texture;
  magnifier: Texture;
}

// Character is one agent's whereabouts: at its desk (drawn by the desk), or
// away as a figure that walks, stands or sits elsewhere.
class Character {
  mode: "seated" | "walking" | "standing" = "seated";
  goal = "desk"; // errandKey of where it is or goes, or "out"
  errand?: Errand;
  place?: Place; // where it stands or sits, once there
  pos: Point = { x: 0, y: 0 }; // feet, in world pixels
  route?: Point[];
  total = 0;
  duration = 1;
  start = 0;
  fadeIn = false;
  figure?: Figure;

  constructor(readonly id: string, public seat: Seat | undefined, public look: FigureLook) {}
}

// Figure is a character away from its desk: legs, body, hair, arms and a prop.
class Figure {
  readonly root = new Container();
  private readonly legs = new Sprite();
  private readonly body = new Sprite();
  private readonly hair = new Sprite();
  private readonly arms = new Graphics();
  private readonly prop = new Sprite();
  private readonly hoverBox = new Graphics();
  private frameKey = "";
  pointerInside = false;

  constructor(private readonly scene: OfficeScene, private readonly agentId: string, private look: FigureLook) {
    this.legs.y = art.body.h;
    this.hoverBox.rect(-2.5, -3.5, art.body.w + 5, art.body.h + art.legs[0].h + 6).stroke({ color: 0x6aa3ff, width: 1, alpha: 0.9 });
    this.hoverBox.visible = false;
    this.prop.visible = false;
    this.root.addChild(this.hoverBox, this.legs, this.body, this.hair, this.arms, this.prop);
    this.root.eventMode = "static";
    this.root.cursor = "pointer";
    this.root.hitArea = new Rectangle(-2, -3, art.body.w + 4, art.body.h + art.legs[0].h + 5);
    this.root.on("pointertap", () => this.scene.open(this.agentId));
    this.root.on("pointerover", () => {
      this.pointerInside = true;
      this.scene.hover(this.agentId);
    });
    this.root.on("pointerout", () => {
      this.pointerInside = false;
      this.scene.hover(undefined);
    });
    this.setLook(look);
  }

  setLook(look: FigureLook): void {
    this.look = look;
    this.hair.texture = look.hair;
    this.frameKey = "";
  }

  setHover(on: boolean): void {
    this.hoverBox.visible = on;
  }

  // placeAt moves the figure; it returns whether it moved.
  private placeAt(x: number, y: number, z: number): boolean {
    const nx = Math.round(x), ny = Math.round(y);
    const moved = nx !== this.root.x || ny !== this.root.y;
    this.root.position.set(nx, ny);
    this.root.zIndex = Math.round(z);
    return moved;
  }

  // walk shows a step: feet at p, legs apart or together, facing away when
  // walking up.
  walk(p: Point, step: number, away: boolean): void {
    this.placeAt(p.x - art.body.w / 2, p.y - art.body.h - art.legs[0].h + 1 - step, p.y);
    const key = `walk${step}${away ? "b" : "f"}`;
    if (key === this.frameKey) return;
    this.frameKey = key;
    this.legs.visible = true;
    this.legs.texture = this.look.legs[step];
    this.body.texture = away ? this.look.back : this.look.front;
    this.prop.visible = false;
    drawArms(this.arms, `walk${step}`, this.look.palette);
  }

  // stand shows the character at its spot; it returns whether the frame changed.
  // At a station it sits behind the furniture and moves with its pose.
  stand(p: Point, place: Place | undefined, plan: FloorPlan, t: number, station?: Station, pose?: Pose): boolean {
    let frame: string;
    let moved: boolean;
    const seated = place === "review" || station !== undefined;
    if (station) {
      moved = this.placeAt(p.x - art.body.w / 2, station.sitY, station.furniture.y + 1);
      frame = pose === "typing" ? `type${Math.floor(t * 6) % 2}`
        : pose === "reviewing" ? `review${Math.floor(t * 1.5) % 2}`
        : "rest0"; // idle, booting: still, so a quiet station draws nothing
    } else if (place === "review" && plan.table) {
      // seated behind the table, which hides the legs
      moved = this.placeAt(p.x - art.body.w / 2, plan.table.y - 15, plan.table.y + 1);
      frame = `review${Math.floor(t * 1.5) % 2}`;
    } else if (place === "queue") {
      // at the user's door, holding what it brings: a plan, or a note
      moved = this.placeAt(p.x - art.body.w / 2, p.y - art.body.h - art.legs[0].h + 1, p.y);
      frame = "hold";
    } else {
      moved = this.placeAt(p.x - art.body.w / 2, p.y - art.body.h - art.legs[0].h + 1, p.y);
      frame = place === "coffee" ? (t % 6 > 4.8 ? "sip" : "hold") : place === "whiteboard" ? "point" : "rest0";
    }
    if (frame === this.frameKey) return moved;
    this.frameKey = frame;
    this.legs.visible = !seated;
    this.legs.texture = this.look.legs[0];
    this.body.texture = this.look.front;
    this.prop.visible = place === "review" || place === "coffee" || place === "queue" || (seated && frame.startsWith("review"));
    if (place === "queue") {
      const plan = this.look.kind === "architect";
      this.prop.texture = plan ? this.look.roll : this.look.note;
      this.prop.position.set(plan ? 5 : 9, 13);
    } else if (place === "review" || (seated && frame.startsWith("review"))) {
      this.prop.texture = this.look.magnifier;
      this.prop.position.set(-7, frame === "review1" ? 6 : 7);
    } else if (place === "coffee") {
      this.prop.texture = this.look.mug;
      if (frame === "sip") this.prop.position.set(8, 7);
      else this.prop.position.set(9, 13);
    }
    drawArms(this.arms, frame, this.look.palette);
    return true;
  }
}

// DeskNode is one desk: furniture, and the agent sitting there if any.
class DeskNode {
  readonly root = new Container();
  private readonly chair: Sprite;
  private readonly body = new Sprite();
  private readonly hair = new Sprite();
  private readonly screen = new Sprite();
  private readonly arms = new Graphics();
  private readonly mug: Sprite;
  private readonly magnifier: Sprite;
  private readonly bubble = new Sprite();
  private readonly hoverBox = new Graphics();
  private seat?: Seat;
  private away = false;
  private frameKey = "";
  private bodyTextures: Texture[] = [];
  private palette: art.Palette = art.basePalette;
  private lastT = 0;

  private readonly screens: Record<Pose, Texture[]>;
  private readonly bubbles: { dots: Texture[]; alert: Texture };

  constructor(
    private readonly scene: OfficeScene,
    private readonly textures: TextureCache,
    readonly slot: Slot,
    private readonly still: boolean,
    private readonly interactive = true, // the user's own desk opens nothing
  ) {
    const t = textures;
    const at = (s: Sprite, p: { x: number; y: number }): Sprite => {
      s.position.set(p.x, p.y);
      return s;
    };
    this.chair = at(new Sprite(slot.kind === "user"
      ? t.get("chair-user", art.chair, { ...art.basePalette, a: 0x3d5a80, A: 0x2a4060 })
      : t.get("chair", art.chair, art.basePalette)), inCell.chair);
    at(this.body, inCell.person);
    at(this.hair, inCell.person);
    const desk = at(new Sprite(t.get("desk", art.desk, art.basePalette)), inCell.desk);
    const monitor = at(new Sprite(t.get("monitor", art.monitor, art.basePalette)), inCell.monitor);
    at(this.screen, { x: inCell.monitor.x + art.screenRect.x, y: inCell.monitor.y + art.screenRect.y });
    const keyboard = at(new Sprite(t.get("keyboard", art.keyboard, art.basePalette)), inCell.keyboard);
    this.arms.position.set(inCell.person.x, inCell.person.y);
    this.mug = at(new Sprite(t.get("mug", art.mug, art.basePalette)), inCell.mug);
    this.magnifier = at(new Sprite(t.get("magnifier", art.magnifier, art.basePalette)), inCell.magnifier);
    at(this.bubble, inCell.bubble);
    const hb = inCell.hit;
    this.hoverBox.rect(hb.x + 0.5, hb.y + 0.5, hb.w - 1, hb.h - 1).stroke({ color: 0x6aa3ff, width: 1, alpha: 0.9 });
    this.hoverBox.visible = false;
    this.root.addChild(this.hoverBox, this.chair, this.body, this.hair, desk, monitor, this.screen, keyboard, this.arms,
      this.mug, this.magnifier, this.bubble);

    const sp = art.screenPalette;
    this.screens = {
      away: t.list("screen-off", () => [art.screenOff()], sp),
      booting: t.list("screen-boot", art.screenBoot, sp),
      idle: t.list("screen-prompt", art.screenPrompt, sp),
      typing: t.frames("screen-code", art.codeMap, sp, art.screenRect.h),
      reviewing: t.list("screen-diff", art.screenDiff, sp),
      waiting: t.list("screen-dialog", art.screenDialog, sp),
    };
    this.bubbles = {
      dots: art.bubbleDots.map((m, i) => t.get(`bubble-dots-${i}`, m, art.basePalette)),
      alert: t.get("bubble-alert", art.bubbleAlert, art.basePalette),
    };

    this.root.hitArea = new Rectangle(hb.x, hb.y, hb.w, hb.h);
    this.root.on("pointertap", () => this.seat && this.scene.open(this.seat.agent.id));
    this.root.on("pointerover", () => this.seat && this.scene.hover(this.seat.agent.id));
    this.root.on("pointerout", () => this.seat && this.scene.hover(undefined));
    this.setSeat(undefined);
  }

  place(grid: Grid): void {
    const o = slotOrigin(this.slot, grid);
    this.root.position.set(o.x, o.y);
    // Characters in the aisle in front of the desk are drawn over it.
    this.root.zIndex = o.y + inCell.desk.y + art.desk.h;
  }

  get agentId(): string | undefined {
    return this.seat?.agent.id;
  }

  setHover(on: boolean): void {
    this.hoverBox.visible = on;
  }

  // setAway empties the chair while the agent's character is elsewhere; the
  // monitor keeps showing its state.
  setAway(away: boolean): void {
    if (this.away === away) return;
    this.away = away;
    this.frameKey = "";
    this.applyPresence();
    this.tick(this.lastT);
  }

  private applyPresence(): void {
    const seat = this.seat;
    const present = seat !== undefined && seat.pose !== "away" && !this.away;
    this.body.visible = present;
    this.hair.visible = present;
    this.arms.visible = present;
    if (!present) {
      this.magnifier.visible = false;
      this.bubble.visible = false;
      this.mug.position.set(inCell.mug.x, inCell.mug.y);
    }
  }

  setSeat(seat: Seat | undefined): void {
    const prev = this.seat;
    this.seat = seat;
    this.root.eventMode = seat && this.interactive ? "static" : "none";
    this.root.cursor = seat && this.interactive ? "pointer" : "default";
    this.root.alpha = seat || !this.interactive ? 1 : 0.8;
    this.magnifier.visible = false;
    this.mug.visible = seat !== undefined;
    this.bubble.visible = false;
    if (!seat) this.setHover(false);
    if (seat && (prev?.agent.id !== seat.agent.id || prev.agent.kind !== seat.agent.kind || prev.agent.role !== seat.agent.role ||
      prev.paletteKey !== seat.paletteKey)) {
      const l = seat.look;
      const palette = seat.palette ?? art.personPalette(art.skins[l.skin], art.hairColors[l.hair], shirtOf(seat.agent));
      const key = seat.paletteKey ?? `${l.skin}-${l.hair}-${seat.agent.kind}-${seat.agent.role}`;
      this.bodyTextures = [
        this.textures.get(`body-${key}`, art.body, palette),
        this.textures.get(`blink-${key}`, art.bodyBlink, palette),
      ];
      this.hair.texture = this.textures.get(`hair-${l.style}-${key}`, art.hair[l.style], palette);
      this.palette = palette;
    }
    if (prev?.agent.id !== seat?.agent.id) this.away = false; // the scene says otherwise right after
    this.applyPresence();
    this.frameKey = ""; // redraw on the next tick
    this.tick(this.lastT);
  }

  // tick shows the frame for time now; it returns whether anything changed.
  tick(now: number): boolean {
    this.lastT = now;
    const seat = this.seat;
    if (!seat) return this.show(this.screen, this.screens.away[0]);
    const t = this.still ? 0 : now + seat.look.phase * 10;
    const pose = seat.pose;
    const frames = this.screens[pose];
    const rate = pose === "typing" ? 6 : pose === "booting" ? 2 : 1.5;
    const screenFrame = Math.floor(t * rate) % frames.length;
    let changed = this.show(this.screen, frames[screenFrame]);
    if (pose === "away" || this.away) return changed;

    const blink = !this.still && t % 4 < 0.15 ? 1 : 0;
    changed = this.show(this.body, this.bodyTextures[blink]) || changed;

    // Pose frame: which arm drawing, mug and prop positions, bubble.
    let frame: string;
    switch (pose) {
      case "typing":
        frame = `type${Math.floor(t * 6) % 2}`;
        break;
      case "waiting":
        frame = `raise${Math.floor(t * 3) % 2}`;
        break;
      case "reviewing":
        frame = `review${Math.floor(t * 1.5) % 2}`;
        break;
      case "idle":
        frame = !this.still && t % 6 > 4.8 ? "sip" : `rest${Math.floor(t * 1.5) % 3}`;
        break;
      default:
        frame = "rest0";
    }
    const key = `${pose}|${frame}`;
    if (key === this.frameKey) return changed;
    this.frameKey = key;

    this.mug.position.set(frame === "sip" ? inCell.mugRaised.x : inCell.mug.x, frame === "sip" ? inCell.mugRaised.y : inCell.mug.y);
    this.magnifier.visible = pose === "reviewing";
    if (pose === "reviewing") {
      const up = frame === "review1" ? 1 : 0;
      this.magnifier.position.set(inCell.magnifier.x, inCell.magnifier.y - up);
    }
    this.bubble.visible = pose === "waiting" || pose === "idle";
    if (pose === "waiting") {
      this.bubble.texture = this.bubbles.alert;
      this.bubble.position.set(inCell.bubbleRaised.x, inCell.bubbleRaised.y - (frame === "raise1" ? 1 : 0));
    } else if (pose === "idle") {
      this.bubble.texture = this.bubbles.dots[frame.startsWith("rest") ? Number(frame.slice(4)) : 2];
      this.bubble.position.set(inCell.bubble.x, inCell.bubble.y);
    }
    drawArms(this.arms, frame, this.palette);
    return true;
  }

  private show(s: Sprite, t: Texture): boolean {
    if (s.texture === t) return false;
    s.texture = t;
    return true;
  }
}

// drawArms draws sleeves and hands for a pose frame (armRects).
function drawArms(g: Graphics, frame: string, p: art.Palette): void {
  g.clear();
  for (const r of armRects(frame, p)) g.rect(r.x, r.y, r.w, r.h).fill(r.color);
}
