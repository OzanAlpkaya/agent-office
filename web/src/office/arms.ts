// Arms of the office's people (ADR 0024), as rectangles. Pure.
import * as art from "./art";

export interface ArmRect {
  x: number;
  y: number;
  w: number;
  h: number;
  color: number;
}

// armRects are the sleeves and hands for a pose frame, in coordinates
// relative to the person sprite (shoulders at y 12, desk top at y 15,
// keyboard at x 2..13, y 18..20; a standing figure's legs start at y 16).
// Pure: the scene draws them with Pixi, the avatar preview on a canvas.
export function armRects(frame: string, p: art.Palette): ArmRect[] {
  const shirt = p.c, shade = p.C, skin = p.s, line = p.k;
  const out: ArmRect[] = [];
  const r = (x: number, y: number, w: number, h: number, color: number): void => {
    out.push({ x, y, w, h, color });
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
  // Arms hanging beside a standing figure; swing moves one hand forward.
  const hanging = (side: "left" | "right", swing: number): void => {
    const x = side === "left" ? 0 : 14;
    r(x, 12, 2, 4 - swing, shade);
    r(x, 16 - swing, 2, 2, skin);
    r(x, 18 - swing, 2, 1, line);
  };
  const raised = (dx: number): void => {
    r(14, 4, 4, 10, line);
    r(15, 5, 2, 9, shirt);
    r(16, 5, 1, 9, shade);
    r(13 + dx, -1, 5, 6, line);
    r(14 + dx, 0, 3, 4, skin);
    r(14 + dx, 4, 3, 1, shirt);
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
    case "raise1":
      // the right arm straight up beside the head, the hand waving
      leftResting(0);
      raised(frame === "raise1" ? 1 : 0);
      break;
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
    case "walk0":
    case "walk1": {
      const s = frame === "walk1" ? 1 : 0;
      hanging("left", s);
      hanging("right", 1 - s);
      break;
    }
    case "point":
      // at the whiteboard: one arm up to the board, a red marker in the hand
      hanging("left", 0);
      r(14, 3, 3, 10, line);
      r(15, 4, 1, 9, shirt);
      r(14, 0, 3, 4, line);
      r(15, 1, 1, 2, skin);
      r(15, -2, 1, 2, 0xe05a5a);
      break;
    case "hold":
      // a mug held in front
      hanging("left", 0);
      r(13, 12, 2, 3, shade);
      r(11, 14, 3, 1, shirt);
      r(10, 14, 2, 2, skin);
      break;
    default:
      leftResting(0);
      rightResting(0);
  }
  return out;
}
