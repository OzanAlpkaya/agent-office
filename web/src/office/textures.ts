// Turns pixel maps into Pixi textures once and keeps them until the office
// closes (ADR 0024).
import { CanvasSource, Rectangle, Texture } from "pixi.js";
import { type Palette, type PixelMap, transparent } from "./art";

function toCanvas(map: PixelMap, palette: Palette): HTMLCanvasElement {
  const canvas = document.createElement("canvas");
  canvas.width = map.w;
  canvas.height = map.h;
  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("office: no 2D canvas context for textures");
  const img = ctx.createImageData(map.w, map.h);
  map.rows.forEach((row, y) => {
    for (let x = 0; x < row.length; x++) {
      const ch = row[x];
      const color = palette[ch];
      if (color === undefined) {
        if (ch === transparent) continue;
        throw new Error(`office: pixel ${JSON.stringify(ch)} is not in the palette`);
      }
      const i = (y * map.w + x) * 4;
      img.data[i] = (color >> 16) & 0xff;
      img.data[i + 1] = (color >> 8) & 0xff;
      img.data[i + 2] = color & 0xff;
      img.data[i + 3] = 0xff;
    }
  });
  ctx.putImageData(img, 0, 0);
  return canvas;
}

// TextureCache keys textures by name, so per-agent variants (palettes) are
// built once and shared by every desk that needs them.
export class TextureCache {
  private readonly textures = new Map<string, Texture>();
  private readonly sources: CanvasSource[] = [];

  get(key: string, map: PixelMap, palette: Palette): Texture {
    let t = this.textures.get(key);
    if (!t) {
      const source = new CanvasSource({ resource: toCanvas(map, palette), scaleMode: "nearest" });
      this.sources.push(source);
      t = new Texture({ source });
      this.textures.set(key, t);
    }
    return t;
  }

  // frames cuts a tall map into windows of height h, one per row offset, for
  // scrolling content such as code on a screen.
  frames(key: string, map: PixelMap, palette: Palette, h: number): Texture[] {
    const base = this.get(key, map, palette);
    const out: Texture[] = [];
    for (let y = 0; y + h <= map.h; y++) {
      const k = `${key}@${y}`;
      let t = this.textures.get(k);
      if (!t) {
        t = new Texture({ source: base.source, frame: new Rectangle(0, y, map.w, h) });
        this.textures.set(k, t);
      }
      out.push(t);
    }
    return out;
  }

  destroy(): void {
    for (const t of this.textures.values()) t.destroy(false);
    for (const s of this.sources) s.destroy();
    this.textures.clear();
    this.sources.length = 0;
  }
}
