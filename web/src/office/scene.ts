// The Pixi scene of the office (ADR 0024). It draws an OfficeModel and
// animates poses; it holds no state of its own beyond what is on screen and
// has no actions except telling its owner which agent was clicked.
// The daemon's Content-Security-Policy has no unsafe-eval (ADR 0040); this pixi.js module
// replaces its generated shader and uniform code with eval-free versions.
import "pixi.js/unsafe-eval";
import { Application, Container, Graphics, Rectangle, Sprite, type Texture, TilingSprite } from "pixi.js";
import type { TaskStatus } from "../api";
import * as art from "./art";
import { type Grid, type Slot, cell, fitGrid, gridFor, inCell, slotOrigin, whiteboardAt, world, worldSize } from "./layout";
import type { Note, OfficeModel, Seat } from "./model";
import type { Pose } from "./pose";
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
}

export interface SceneOptions {
  reducedMotion: boolean;
  onOpen: (agentId: string) => void;
  onHover: (agentId: string | undefined) => void;
}

const noteColors: Record<TaskStatus, art.Swatch> = {
  todo: { base: 0xf2d16b, shade: 0xc9a63c },
  in_progress: { base: 0x8ab4f8, shade: 0x5a8de0 },
  review: { base: 0xc49bff, shade: 0x9b6dde },
  done: { base: 0x8fd694, shade: 0x5cb860 },
  blocked: { base: 0xf28b82, shade: 0xd05a50 },
};

const background = 0x0f1115;

export class OfficeScene {
  private readonly textures = new TextureCache();
  private readonly world = new Container();
  private readonly floor: TilingSprite;
  private readonly decor = new Container();
  private readonly notes = new Container();
  private readonly desks = new Container();
  private readonly flying = new Container();
  private readonly nodes: DeskNode[] = [];
  private readonly envelopes: { sprite: Sprite; from: Point; to: Point; start: number }[] = [];
  private grid: Grid = gridFor(0, 4);
  private wall: TilingSprite;
  private model?: OfficeModel;
  private hovered?: string;
  private size = { w: 1, h: 1 };
  private destroyed = false;
  private dirty = true;
  private notesKey = "";
  private ticks = 0;
  private renders = 0;
  private readonly costs: number[] = []; // ms of the last rendered frames

  private constructor(private readonly app: Application, private readonly opts: SceneOptions) {
    const t = this.textures;
    this.floor = new TilingSprite({ texture: t.get("floor", art.floorTile, art.floorPalette), width: 1, height: 1 });
    this.floor.y = art.wallHeight;
    this.wall = new TilingSprite({ texture: t.get("wall", art.wallTile, art.wallPalette), width: 1, height: art.wallHeight });
    this.world.addChild(this.floor, this.wall, this.decor, this.desks, this.flying);
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
    const sorted = this.costs.slice().sort((a, b) => a - b);
    return {
      ticks: this.ticks,
      renders: this.renders,
      p95Ms: sorted.length > 0 ? sorted[Math.floor(sorted.length * 0.95)] : 0,
      maxMs: sorted.length > 0 ? sorted[sorted.length - 1] : 0,
    };
  }

  private frame(): void {
    const start = performance.now();
    this.ticks++;
    const changed = this.tick(start / 1000);
    if (!changed && !this.dirty) return;
    this.dirty = false;
    this.app.render();
    this.renders++;
    this.costs.push(performance.now() - start);
    if (this.costs.length > 300) this.costs.shift();
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
    return this.layout();
  }

  // layout picks the grid and a whole number of device pixels per world
  // pixel, so pixels stay square and sharp, and rebuilds what depends on the
  // grid when it changed.
  private layout(): Transform {
    // Read the ratio each time: browser zoom and moving the window to another
    // screen change it.
    const dpr = window.devicePixelRatio || 1;
    const { w, h } = this.size;
    const { grid, k } = fitGrid(w, h, dpr, this.model?.desks ?? 0);
    if (grid.columns !== this.grid.columns || grid.rows !== this.grid.rows || this.nodes.length === 0) this.setGrid(grid);
    const { w: W, h: H } = worldSize(grid);
    const scale = k / dpr;
    const width = Math.max(w, Math.ceil(W * scale));
    const height = Math.max(h, Math.ceil(H * scale));
    const x = Math.round(((width - W * scale) / 2) * dpr) / dpr;
    const y = Math.round(((height - H * scale) / 2) * dpr) / dpr;
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
    this.model = model;
    const t = this.layout();
    const bySlot = new Map(model.seats.map((s) => [slotKey(s.slot), s]));
    for (const n of this.nodes) n.setSeat(bySlot.get(slotKey(n.slot)));
    const notes = model.goal?.notes ?? [];
    const notesKey = `${model.goal !== undefined}|${notes.map((n) => `${n.id}:${n.status}`).join(",")}`;
    if (notesKey !== this.notesKey) {
      this.notesKey = notesKey;
      this.drawNotes(notes);
    }
    this.dirty = true;
    if (this.hovered && !model.seats.some((s) => s.agent.id === this.hovered)) this.hover(undefined);
    return t.grid.columns !== before.columns || t.grid.rows !== before.rows ? t : undefined;
  }

  // sendEnvelope flies a letter from one agent's desk to another's; a
  // missing sender (agent-office itself) sends from the door.
  sendEnvelope(fromAgent: string | undefined, toAgent: string): void {
    if (this.opts.reducedMotion || this.envelopes.length >= maxEnvelopes) return;
    const to = this.deskPoint(toAgent);
    if (!to) return;
    const door = doorAt(this.grid);
    const from = (fromAgent !== undefined ? this.deskPoint(fromAgent) : undefined) ?? { x: door.x + 6, y: art.wallHeight - 6 };
    const sprite = new Sprite(this.textures.get("envelope", art.envelope, art.basePalette));
    this.flying.addChild(sprite);
    this.envelopes.push({ sprite, from, to, start: performance.now() / 1000 });
  }

  private deskPoint(agentId: string): Point | undefined {
    const seat = this.model?.seats.find((s) => s.agent.id === agentId);
    if (!seat) return undefined;
    const o = slotOrigin(seat.slot, this.grid);
    return { x: o.x + inCell.person.x + 4, y: o.y + inCell.person.y };
  }

  hover(agentId: string | undefined): void {
    if (this.hovered === agentId) return;
    this.hovered = agentId;
    for (const n of this.nodes) n.setHover(n.agentId !== undefined && n.agentId === agentId);
    this.dirty = true;
    this.opts.onHover(agentId);
  }

  private setGrid(grid: Grid): void {
    this.grid = grid;
    const { w, h } = worldSize(grid);
    this.wall.width = w;
    this.floor.width = w;
    this.floor.height = h - art.wallHeight;
    const want = 1 + grid.rows * grid.columns;
    while (this.nodes.length < want) {
      const i = this.nodes.length;
      const slot: Slot = i === 0 ? { kind: "lead" } : { kind: "desk", index: i - 1 };
      const node = new DeskNode(this, this.textures, slot, this.opts.reducedMotion);
      this.nodes.push(node);
      this.desks.addChild(node.root);
    }
    while (this.nodes.length > want) this.nodes.pop()?.root.destroy({ children: true });
    // Nodes are added in desk order, so rows further back are drawn first.
    for (const n of this.nodes) n.place(grid);
    this.buildDecor(grid);
  }

  private buildDecor(grid: Grid): void {
    const t = this.textures;
    this.decor.removeChildren().forEach((c) => {
      if (c !== this.notes) c.destroy();
    });
    const put = (tex: Texture, x: number, y: number): Sprite => {
      const s = new Sprite(tex);
      s.position.set(x, y);
      this.decor.addChild(s);
      return s;
    };
    const { w, h } = worldSize(grid);
    const lead = slotOrigin({ kind: "lead" }, grid);
    put(t.get("rug", () => art.rug(cell.w + 24, world.leadRowH - 6), art.rugPalette), lead.x - 12, world.leadRowY + 2);
    // Windows left of the whiteboard, and right of it where the door leaves room.
    const wb = whiteboardAt(grid);
    const door = doorAt(grid);
    const win = t.get("window", art.windowMap, art.basePalette);
    for (let x = wb.x - 44; x >= 20; x -= 44) put(win, x, 8);
    for (let x = wb.x + art.whiteboardSize.w + 12; x + art.windowMap.w <= door.x - 8; x += 44) put(win, x, 8);
    put(t.get("whiteboard", art.whiteboardMap, art.basePalette), wb.x, wb.y);
    this.notes.position.set(wb.x + art.noteGrid.x, wb.y + art.noteGrid.y);
    this.decor.addChild(this.notes);
    put(t.get("door", art.door, art.basePalette), door.x, door.y);
    put(t.get("cooler", art.waterCooler, art.basePalette), w - 14, art.wallHeight + 2);
    const plant = t.get("plant", art.plant, art.basePalette);
    put(plant, 2, art.wallHeight - 8);
    put(plant, 2, h - world.bottom - art.plant.h);
    put(plant, w - 14, h - world.bottom - art.plant.h);
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

  // tick advances the animations; it returns whether anything changed.
  private tick(now: number): boolean {
    const t = this.opts.reducedMotion ? 0 : now;
    let changed = this.envelopes.length > 0;
    for (const n of this.nodes) changed = n.tick(t) || changed;
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
    return changed;
  }

  // Called by desks.
  open(agentId: string): void {
    this.opts.onOpen(agentId);
  }
}

interface Point {
  x: number;
  y: number;
}

// The door stands at the right end of the wall.
function doorAt(g: Grid): Point {
  return { x: worldSize(g).w - art.door.w - 22, y: art.wallHeight - art.door.h };
}
const envelopeSeconds = 1.4;
// Envelopes in flight at most; more are skipped (e.g. many messages while the
// tab was in the background).
const maxEnvelopes = 20;

const slotKey = (s: Slot): string => (s.kind === "lead" ? "lead" : `desk-${s.index}`);

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
  ) {
    const t = textures;
    const at = (s: Sprite, p: { x: number; y: number }): Sprite => {
      s.position.set(p.x, p.y);
      return s;
    };
    this.chair = at(new Sprite(t.get("chair", art.chair, art.basePalette)), inCell.chair);
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
  }

  get agentId(): string | undefined {
    return this.seat?.agent.id;
  }

  setHover(on: boolean): void {
    this.hoverBox.visible = on;
  }

  setSeat(seat: Seat | undefined): void {
    const prev = this.seat;
    this.seat = seat;
    const present = seat !== undefined && seat.pose !== "away";
    this.root.eventMode = seat ? "static" : "none";
    this.root.cursor = seat ? "pointer" : "default";
    this.root.alpha = seat ? 1 : 0.8;
    this.body.visible = present;
    this.hair.visible = present;
    this.arms.visible = present;
    this.magnifier.visible = false;
    this.mug.visible = seat !== undefined;
    this.bubble.visible = false;
    if (!seat) this.setHover(false);
    if (seat && (prev?.agent.id !== seat.agent.id || prev.agent.kind !== seat.agent.kind)) {
      const l = seat.look;
      const palette = art.personPalette(art.skins[l.skin], art.hairColors[l.hair], art.shirts[seat.agent.kind]);
      const key = `${l.skin}-${l.hair}-${seat.agent.kind}`;
      this.bodyTextures = [
        this.textures.get(`body-${key}`, art.body, palette),
        this.textures.get(`blink-${key}`, art.bodyBlink, palette),
      ];
      this.hair.texture = this.textures.get(`hair-${l.style}-${key}`, art.hair[l.style], palette);
      this.palette = palette;
    }
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
    if (pose === "away") {
      this.mug.position.set(inCell.mug.x, inCell.mug.y);
      return changed;
    }

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

// drawArms draws sleeves and hands for a pose frame, in coordinates relative
// to the person sprite (shoulders at y 12, desk top at y 15, keyboard at
// x 2..13, y 18..20).
function drawArms(g: Graphics, frame: string, p: art.Palette): void {
  const shirt = p.c, shade = p.C, skin = p.s, line = p.k;
  g.clear();
  const r = (x: number, y: number, w: number, h: number, color: number): void => {
    g.rect(x, y, w, h).fill(color);
  };
  const leftResting = (lift: number): void => {
    r(1, 13, 2, 3, shade);
    r(2, 16, 3, 1, shirt);
    r(4, 17 - lift, 2, 2, skin);
    r(4, 19 - lift, 2, 1, line);
  };
  const rightResting = (lift: number): void => {
    r(13, 13, 2, 3, shade);
    r(11, 16, 3, 1, shirt);
    r(10, 17 - lift, 2, 2, skin);
    r(10, 19 - lift, 2, 1, line);
  };
  switch (frame) {
    case "type0":
      leftResting(1);
      rightResting(0);
      break;
    case "type1":
      leftResting(0);
      rightResting(1);
      break;
    case "raise0":
    case "raise1": {
      // the right arm straight up beside the head, the hand waving
      const dx = frame === "raise1" ? 1 : 0;
      leftResting(0);
      r(14, 4, 4, 10, line);
      r(15, 5, 2, 9, shirt);
      r(16, 5, 1, 9, shade);
      r(13 + dx, -1, 5, 6, line);
      r(14 + dx, 0, 3, 4, skin);
      r(14 + dx, 4, 3, 1, shirt);
      break;
    }
    case "review0":
    case "review1": {
      const up = frame === "review1" ? 1 : 0;
      r(-1, 13, 3, 2, shade);
      r(-3, 14 - up, 2, 2, skin);
      rightResting(0);
      break;
    }
    case "sip":
      leftResting(0);
      r(13, 13, 2, 2, shade);
      r(11, 11, 2, 2, shirt);
      r(10, 10, 2, 2, skin);
      break;
    default:
      leftResting(0);
      rightResting(0);
  }
}
