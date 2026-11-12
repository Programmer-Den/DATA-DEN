import { Item, takenAt } from './item';

/** Photos that belong together, made from where and when they were taken. */
export interface Album {
  id: string;
  name: string;
  /** Newest first, like the timeline. */
  items: Item[];
  /** When the oldest and the newest photo were taken; null when empty. */
  from: Date | null;
  to: Date | null;
  /** True for an album made by hand (/api/albums). */
  own?: boolean;
}

/** An own album as the server keeps it. */
export interface OwnAlbum {
  id: string;
  name: string;
  created: string;
  paths: string[];
}

/** A trip ends when no photo away from home was taken for this long. */
export const TRIP_GAP_MS = 3 * 24 * 60 * 60 * 1000;
/** Fewer photos away from home are not a trip. */
export const MIN_TRIP = 3;

function album(id: string, name: string, items: Item[]): Album {
  const times = items.map((item) => takenAt(item).getTime());
  return {
    id,
    name,
    items: [...items].sort((a, b) => takenAt(b).getTime() - takenAt(a).getTime()),
    from: times.length ? new Date(Math.min(...times)) : null,
    to: times.length ? new Date(Math.max(...times)) : null,
  };
}

/**
 * The own albums with their photos, newest album first. Paths of files that
 * are gone, or in the recycle bin, are left out.
 */
export function ownAlbums(own: OwnAlbum[], items: Item[]): Album[] {
  const byPath = new Map(items.map((item) => [item.path, item]));
  return [...own]
    .sort((a, b) => b.created.localeCompare(a.created))
    .map((o) => ({
      ...album(
        o.id,
        o.name,
        o.paths.flatMap((p) => byPath.get(p) ?? []),
      ),
      own: true,
    }));
}

/** The places of the photos, most photos first. */
function byCount(items: Item[]): string[] {
  const counts = new Map<string, number>();
  for (const item of items) {
    if (item.place) {
      counts.set(item.place, (counts.get(item.place) ?? 0) + 1);
    }
  }
  return [...counts].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).map(([p]) => p);
}

/** One album per town, the town with the most photos first. */
export function placeAlbums(items: Item[]): Album[] {
  const byPlace = new Map<string, Item[]>();
  for (const item of items) {
    if (item.place) {
      byPlace.set(item.place, [...(byPlace.get(item.place) ?? []), item]);
    }
  }
  return byCount(items).map((place) => album('place:' + place, place, byPlace.get(place)!));
}

/**
 * Trips: photos taken away from home, home being the town with the most
 * photos. A photo at home or a gap of TRIP_GAP_MS ends a trip; photos
 * without a position belong to none. Newest first.
 */
export function tripAlbums(items: Item[]): Album[] {
  const home = byCount(items)[0];
  const placed = items
    .filter((item) => item.place)
    .sort((a, b) => takenAt(a).getTime() - takenAt(b).getTime());
  const trips: Item[][] = [];
  let current: Item[] = [];
  const end = () => {
    if (current.length >= MIN_TRIP) {
      trips.push(current);
    }
    current = [];
  };
  for (const item of placed) {
    if (item.place === home) {
      end();
      continue;
    }
    const last = current.at(-1);
    if (last && takenAt(item).getTime() - takenAt(last).getTime() > TRIP_GAP_MS) {
      end();
    }
    current.push(item);
  }
  end();
  return trips
    .map((trip) => album('trip:' + trip[0].path, byCount(trip).slice(0, 3).join(', '), trip))
    .sort((a, b) => b.to!.getTime() - a.to!.getTime());
}

/**
 * The photos of one spot on the map, named after their towns, such as
 * "Florenz, Siena", or "3 Fotos" when no town is known.
 */
export function spotAlbum(items: Item[]): Album {
  const places = byCount(items).slice(0, 2);
  const name = places.length
    ? places.join(', ')
    : items.length === 1
      ? '1 Foto'
      : `${items.length} Fotos`;
  return album('map:' + items.map((item) => item.path).join('|'), name, items);
}
