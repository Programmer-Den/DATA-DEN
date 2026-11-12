import { placeAlbums, spotAlbum, tripAlbums } from './albums';
import { Item } from './item';

function photo(name: string, taken: string, place?: string): Item {
  return { path: '/' + name, name, size: 1, modified: '2026-10-01T00:00:00Z', taken, place };
}

describe('albums', () => {
  const items = [
    photo('h1.jpg', '2026-01-01T10:00:00', 'Freiburg'),
    photo('h2.jpg', '2026-02-01T10:00:00', 'Freiburg'),
    photo('h3.jpg', '2026-03-01T10:00:00', 'Freiburg'),
    photo('h4.jpg', '2026-08-01T10:00:00', 'Freiburg'),
    // A week in Italy, with a photo without a position on the way.
    photo('r1.jpg', '2026-07-01T10:00:00', 'Rom'),
    photo('r2.jpg', '2026-07-02T10:00:00', 'Rom'),
    photo('x.jpg', '2026-07-03T10:00:00'),
    photo('f1.jpg', '2026-07-04T10:00:00', 'Florenz'),
    photo('f2.jpg', '2026-07-06T10:00:00', 'Florenz'),
    // Two photos away are no trip.
    photo('b1.jpg', '2026-05-01T10:00:00', 'Basel'),
    photo('b2.jpg', '2026-05-01T11:00:00', 'Basel'),
    // A gap of a month splits trips.
    photo('m1.jpg', '2025-06-01T10:00:00', 'München'),
    photo('m2.jpg', '2025-06-02T10:00:00', 'München'),
    photo('m3.jpg', '2025-06-03T10:00:00', 'München'),
    photo('m4.jpg', '2025-07-10T10:00:00', 'München'),
  ];

  it('makes an album per place, most photos first', () => {
    const albums = placeAlbums(items);
    expect(albums.map((a) => [a.name, a.items.length])).toEqual([
      ['Freiburg', 4],
      ['München', 4],
      ['Basel', 2],
      ['Florenz', 2],
      ['Rom', 2],
    ]);
    expect(albums[0].items[0].name).toBe('h4.jpg');
    expect(albums[0].from).toEqual(new Date('2026-01-01T10:00:00'));
    expect(albums[0].to).toEqual(new Date('2026-08-01T10:00:00'));
  });

  it('finds trips away from home, newest first', () => {
    const trips = tripAlbums(items);
    expect(trips.map((t) => t.name)).toEqual(['Florenz, Rom', 'München']);
    expect(trips[0].items.map((i) => i.name)).toEqual(['f2.jpg', 'f1.jpg', 'r2.jpg', 'r1.jpg']);
    expect(trips[1].items.length).toBe(3);
  });

  it('names the photos of a spot on the map after their towns', () => {
    expect(spotAlbum(items.slice(4, 9)).name).toBe('Florenz, Rom');
    expect(spotAlbum([photo('a.jpg', '2026-01-01T10:00:00')]).name).toBe('1 Foto');
    const two = spotAlbum([
      photo('a.jpg', '2026-01-01T10:00:00'),
      photo('b.jpg', '2026-01-02T10:00:00'),
    ]);
    expect(two.name).toBe('2 Fotos');
    expect(two.items.map((i) => i.name)).toEqual(['b.jpg', 'a.jpg']);
  });

  it('finds nothing without positions', () => {
    const none = [photo('a.jpg', '2026-01-01T10:00:00')];
    expect(placeAlbums(none)).toEqual([]);
    expect(tripAlbums(none)).toEqual([]);
  });
});
