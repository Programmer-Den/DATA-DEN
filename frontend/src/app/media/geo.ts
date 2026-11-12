import { Item } from './item';

/** The size of a map tile in pixels. */
export const TILE = 256;
/** The closest zoom the tile server draws. */
export const MAX_ZOOM = 19;
/** The closest zoom the map starts with, so one photo shows its town. */
export const START_ZOOM = 15;
/** Photos closer than this many pixels on the map are one pin. */
export const PIN_CELL = 64;

/** Web Mercator ends at about 85° north and south. */
const MAX_LAT = 85.05112878;

/** A point of the world in 0 to 1 from the top left (180° west, 85° north). */
export interface Point {
  x: number;
  y: number;
}

/** What the map shows: the point in its middle and the zoom. */
export interface View {
  center: Point;
  zoom: number;
}

/** A photo with a position. */
export type Placed = Item & { lat: number; lon: number };

/** Photos close together on the map, drawn as one pin. */
export interface Pin {
  /** Where the pin is, in pixels of the whole world at the zoom. */
  x: number;
  y: number;
  /** Newest first, like the timeline. */
  items: Placed[];
}

/** A tile to draw and where, in pixels from the map's top left. */
export interface Tile {
  url: string;
  left: number;
  top: number;
}

/** Where lat, lon is on the Web Mercator map. */
export function project(lat: number, lon: number): Point {
  const phi = (Math.max(-MAX_LAT, Math.min(MAX_LAT, lat)) * Math.PI) / 180;
  return {
    x: (lon + 180) / 360,
    y: (1 - Math.log(Math.tan(phi) + 1 / Math.cos(phi)) / Math.PI) / 2,
  };
}

/** The size of the whole world in pixels at zoom. */
export function worldSize(zoom: number): number {
  return TILE * 2 ** zoom;
}

export function hasPosition(item: Item): item is Placed {
  return typeof item.lat === 'number' && typeof item.lon === 'number';
}

/**
 * The view that shows all points in a map of width × height pixels, as
 * close as it can but not closer than START_ZOOM; null for no points.
 */
export function fit(points: Point[], width: number, height: number): View | null {
  if (!points.length) {
    return null;
  }
  const xs = points.map((p) => p.x);
  const ys = points.map((p) => p.y);
  const [minX, maxX, minY, maxY] = [
    Math.min(...xs),
    Math.max(...xs),
    Math.min(...ys),
    Math.max(...ys),
  ];
  const center = { x: (minX + maxX) / 2, y: (minY + maxY) / 2 };
  // Room for the pins at the edges.
  const room = (size: number) => Math.max(size - PIN_CELL * 2, PIN_CELL);
  let zoom = START_ZOOM;
  while (
    zoom > 0 &&
    ((maxX - minX) * worldSize(zoom) > room(width) ||
      (maxY - minY) * worldSize(zoom) > room(height))
  ) {
    zoom--;
  }
  return { center, zoom };
}

/**
 * The photos as pins at zoom: those in the same square of PIN_CELL pixels
 * are one pin, at their middle. The pins keep the order of items.
 */
export function pins(items: Placed[], zoom: number): Pin[] {
  const size = worldSize(zoom);
  const byCell = new Map<string, { sx: number; sy: number; items: Placed[] }>();
  for (const item of items) {
    const p = project(item.lat, item.lon);
    const x = p.x * size;
    const y = p.y * size;
    const cell = `${Math.floor(x / PIN_CELL)},${Math.floor(y / PIN_CELL)}`;
    const pin = byCell.get(cell);
    if (pin) {
      pin.sx += x;
      pin.sy += y;
      pin.items.push(item);
    } else {
      byCell.set(cell, { sx: x, sy: y, items: [item] });
    }
  }
  return [...byCell.values()].map((p) => ({
    x: p.sx / p.items.length,
    y: p.sy / p.items.length,
    items: p.items,
  }));
}

/** The tiles that cover a map of width × height pixels showing view. */
export function tiles(view: View, width: number, height: number): Tile[] {
  const size = worldSize(view.zoom);
  const n = 2 ** view.zoom;
  const left = view.center.x * size - width / 2;
  const top = view.center.y * size - height / 2;
  const out: Tile[] = [];
  const lastY = Math.min(n - 1, Math.ceil((top + height) / TILE) - 1);
  for (let ty = Math.max(0, Math.floor(top / TILE)); ty <= lastY; ty++) {
    for (let tx = Math.floor(left / TILE); tx < Math.ceil((left + width) / TILE); tx++) {
      // East of 180° the world starts again.
      const x = ((tx % n) + n) % n;
      out.push({
        url: `/api/tiles/${view.zoom}/${x}/${ty}`,
        left: tx * TILE - left,
        top: ty * TILE - top,
      });
    }
  }
  return out;
}

/**
 * The view at zoom, keeping the point at (px, py) pixels from the map's
 * middle where it is, as when zooming at the mouse.
 */
export function zoomAt(view: View, zoom: number, px: number, py: number): View {
  zoom = Math.max(0, Math.min(MAX_ZOOM, zoom));
  const before = worldSize(view.zoom);
  const after = worldSize(zoom);
  const x = view.center.x + px / before;
  const y = view.center.y + py / before;
  return { zoom, center: clampCenter({ x: x - px / after, y: y - py / after }) };
}

/** Keeps the middle of the map within the world, north to south. */
export function clampCenter(center: Point): Point {
  return { x: center.x - Math.floor(center.x), y: Math.max(0, Math.min(1, center.y)) };
}
