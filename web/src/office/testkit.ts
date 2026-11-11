// Shared by the floor plan tests (floor.test.ts, custom.test.ts): where
// characters must be able to stand and walk to.
import { type FloorPlan, type Point, deskExit, stationKinds } from "./floor";

export const exitOf = (o: Point): Point => ({ x: o.x + deskExit.x, y: o.y + deskExit.y });

export function targets(p: FloorPlan): [string, Point][] {
  return [
    ["entrance", p.entrance],
    ...p.spots.whiteboard.map((s, i): [string, Point] => [`whiteboard ${i}`, s]),
    ...p.spots.coffee.map((s, i): [string, Point] => [`coffee ${i}`, s]),
    ...p.spots.review.map((s, i): [string, Point] => [`review ${i}`, s]),
    ...p.spots.queue.filter((_, i) => i % 3 === 0).map((s, i): [string, Point] => [`queue ${i * 3}`, s]),
    ...stationKinds.flatMap((k): [string, Point][] => {
      const st = p.stations[k];
      return st ? [[`${k} station`, st.seat]] : [];
    }),
    ["lead desk", exitOf(p.lead)],
  ];
}
