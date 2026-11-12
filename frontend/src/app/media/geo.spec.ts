import { Placed, fit, pins, project, tiles, worldSize, zoomAt, START_ZOOM } from './geo';

function placed(name: string, lat: number, lon: number): Placed {
  return { path: '/' + name, name, size: 1, modified: '2026-10-01T00:00:00Z', lat, lon };
}

describe('geo', () => {
  it('projects positions like the tile servers', () => {
    expect(project(0, 0)).toEqual({ x: 0.5, y: 0.5 });
    // Berlin is in tile 8/137/83 of OpenStreetMap.
    const berlin = project(52.52, 13.4);
    expect(Math.floor(berlin.x * 256)).toBe(137);
    expect(Math.floor(berlin.y * 256)).toBe(83);
    // The poles are cut off at about 85°.
    expect(project(90, 0).y).toBeCloseTo(0, 6);
    expect(project(-90, 0).y).toBeCloseTo(1, 6);
  });

  it('fits all photos into the map', () => {
    expect(fit([], 800, 480)).toBeNull();
    // One photo: as close as START_ZOOM.
    expect(fit([project(52.52, 13.4)], 800, 480)?.zoom).toBe(START_ZOOM);
    const rome = project(41.9, 12.5);
    const berlin = project(52.52, 13.4);
    const view = fit([rome, berlin], 800, 480)!;
    expect(view.center.x).toBeCloseTo((rome.x + berlin.x) / 2);
    const span = (rome.y - berlin.y) * worldSize(view.zoom);
    expect(span).toBeLessThanOrEqual(480 - 128);
    expect(span * 2).toBeGreaterThan(480 - 128);
  });

  it('makes one pin of close photos', () => {
    const items = [
      placed('a.jpg', 41.9, 12.5),
      placed('b.jpg', 41.9001, 12.5001),
      placed('c.jpg', 52.52, 13.4),
    ];
    expect(pins(items, 10).map((p) => p.items.map((i) => i.name))).toEqual([
      ['a.jpg', 'b.jpg'],
      ['c.jpg'],
    ]);
    // Close enough, every photo is its own pin.
    expect(pins(items, 19).length).toBe(3);
    // From far away, all are one.
    expect(pins(items, 0).length).toBe(1);
  });

  it('covers the map with tiles, around the world', () => {
    const all = tiles({ center: { x: 0.5, y: 0.5 }, zoom: 1 }, 512, 512);
    expect(all.map((t) => [t.url, t.left, t.top])).toEqual([
      ['/api/tiles/1/0/0', 0, 0],
      ['/api/tiles/1/1/0', 256, 0],
      ['/api/tiles/1/0/1', 0, 256],
      ['/api/tiles/1/1/1', 256, 256],
    ]);
    // West of 180° are the tiles of the east, and nothing above the north.
    const edge = tiles({ center: { x: 0, y: 0 }, zoom: 2 }, 256, 256);
    expect(edge.map((t) => t.url)).toEqual(['/api/tiles/2/3/0', '/api/tiles/2/0/0']);
  });

  it('zooms at a point and keeps it in place', () => {
    const view = { center: { x: 0.5, y: 0.5 }, zoom: 4 };
    const closer = zoomAt(view, 5, 100, -50);
    // The point 100 px right of the middle stays 100 px right of it.
    const before = 0.5 + 100 / worldSize(4);
    const after = closer.center.x + 100 / worldSize(5);
    expect(after).toBeCloseTo(before, 10);
    expect(zoomAt(view, 25, 0, 0).zoom).toBe(19);
    expect(zoomAt(view, -1, 0, 0).zoom).toBe(0);
  });
});
