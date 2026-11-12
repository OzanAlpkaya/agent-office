// Draws the office's pixel maps onto a 2D canvas: for the scene's textures
// and for the avatar preview in the settings (Phase 18 review).
import { type Palette, type PixelMap, transparent } from "./art";

export function drawMap(ctx: CanvasRenderingContext2D, map: PixelMap, palette: Palette, x0: number, y0: number): void {
  const img = ctx.getImageData(x0, y0, map.w, map.h);
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
  ctx.putImageData(img, x0, y0);
}
