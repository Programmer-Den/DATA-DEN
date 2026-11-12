import { HttpClient } from '@angular/common/http';
import { Component, computed, effect, inject, input, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { formatSize } from '../format';
import { Album, OwnAlbum, ownAlbums, placeAlbums, spotAlbum, tripAlbums } from './albums';
import { Placed, View, hasPosition } from './geo';
import { Item, Kind, downloadUrl, takenAt, thumbUrl, viewUrl } from './item';
import { PhotoMap } from './map';
import { Viewer } from './viewer';

interface Media {
  kind: Kind;
  items: Item[];
}

/** Photos of one month, such as "Oktober 2026". */
interface Month {
  name: string;
  /** Set on the newest month of a year, the target of the year list. */
  year?: number;
  items: { item: Item; index: number }[];
}

/** A year of the timeline and the index of its newest photo. */
interface Year {
  year: number;
  first: number;
}

/**
 * How the photos are shown: all by month, on a map, or as albums by place,
 * by trip or made by hand.
 */
export type Mode = 'timeline' | 'map' | 'places' | 'trips' | 'own';

/** Files shown at first and added by "Mehr anzeigen", so thousands of photos stay quick. */
export const PAGE = 120;

const monthName = new Intl.DateTimeFormat('de-DE', { month: 'long', year: 'numeric' });
const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium' });

/**
 * Every photo, video or document of the data directory, newest first: photos
 * as a timeline by month taken with a list of years to jump to, or as albums
 * by place and by trip; videos as tiles, documents as a list. A click opens
 * the viewer. The map shows where the photos were taken; a click on a pin
 * shows its photos.
 */
@Component({
  selector: 'app-media',
  imports: [PhotoMap, Viewer],
  templateUrl: './media.html',
  styleUrl: './media.css',
})
export class MediaView {
  private readonly http = inject(HttpClient);

  readonly kind = input.required<Kind>();

  protected readonly items = signal<Item[] | null>(null);
  protected readonly error = signal<string | null>(null);
  /** Shown only files whose path contains this, ignoring case. */
  protected readonly search = signal('');
  protected readonly limit = signal(PAGE);
  /** The file open in the viewer, an index into found, or null. */
  protected readonly open = signal<number | null>(null);
  /** Photos whose thumbnail failed; they show the photo itself. */
  private readonly failedThumbs = signal(new Set<string>());

  protected readonly mode = signal<Mode>('timeline');
  /** The own albums from the server, null until loaded. */
  protected readonly own = signal<OwnAlbum[] | null>(null);
  /** The id of the album open, whose photos are shown; null shows the timeline or the albums. */
  private readonly albumId = signal<string | null>(null);
  /** The albums of the mode chosen. */
  private readonly modeAlbums = computed<Album[]>(() => {
    const items = this.items() ?? [];
    switch (this.mode()) {
      case 'places':
        return placeAlbums(items);
      case 'trips':
        return tripAlbums(items);
      case 'own':
        return ownAlbums(this.own() ?? [], items);
      default:
        return [];
    }
  });
  /** The photos of the pin picked on the map, shown like an album. */
  private readonly spot = signal<Album | null>(null);
  protected readonly album = computed(
    () => this.spot() ?? this.modeAlbums().find((a) => a.id === this.albumId()) ?? null,
  );
  /** What the map shows, kept while a pin's photos are open. */
  protected readonly mapView = signal<View | null>(null);
  /** Paths of the photos chosen, while choosing; null otherwise. */
  protected readonly selected = signal<Set<string> | null>(null);
  /** The own album that "Hinzufügen" adds to. */
  protected readonly target = signal('');
  /** What the last change of an own album did, or why it failed. */
  protected readonly notice = signal<string | null>(null);
  /** True while the open own album's name is edited. */
  protected readonly renaming = signal(false);

  /** Every file, or the photos of the album open. */
  private readonly all = computed(() => this.album()?.items ?? this.items() ?? []);
  protected readonly found = computed(() => {
    const search = this.search().trim().toLowerCase();
    const items = this.all();
    return search ? items.filter((item) => item.path.toLowerCase().includes(search)) : items;
  });
  /** The albums of the mode chosen, found by name or by a photo's path. */
  protected readonly albums = computed<Album[]>(() => {
    const albums = this.modeAlbums();
    const search = this.search().trim().toLowerCase();
    return search
      ? albums.filter(
          (album) =>
            album.name.toLowerCase().includes(search) ||
            album.items.some((item) => item.path.toLowerCase().includes(search)),
        )
      : albums;
  });
  /** True while the album list is shown instead of photos. */
  protected readonly showAlbums = computed(
    () =>
      this.kind() === 'photo' &&
      !['timeline', 'map'].includes(this.mode()) &&
      this.album() === null,
  );
  /** True while the map is shown instead of photos. */
  protected readonly showMap = computed(
    () => this.kind() === 'photo' && this.mode() === 'map' && this.album() === null,
  );
  /** The photos found that have a position, for the map. */
  protected readonly placed = computed<Placed[]>(() => this.found().filter(hasPosition));
  protected readonly shown = computed(() => this.found().slice(0, this.limit()));
  protected readonly months = computed<Month[]>(() => {
    const months: Month[] = [];
    let year: number | undefined;
    this.shown().forEach((item, index) => {
      const when = takenAt(item);
      const name = monthName.format(when);
      if (months.at(-1)?.name !== name) {
        const newYear = when.getFullYear() !== year ? when.getFullYear() : undefined;
        year = when.getFullYear();
        months.push({ name, year: newYear, items: [] });
      }
      months.at(-1)!.items.push({ item, index });
    });
    return months;
  });
  /** The years of all photos found, newest first, for jumping in the timeline. */
  protected readonly years = computed<Year[]>(() => {
    const years: Year[] = [];
    this.found().forEach((item, index) => {
      const year = takenAt(item).getFullYear();
      if (!years.some((y) => y.year === year)) {
        years.push({ year, first: index });
      }
    });
    return years;
  });

  protected readonly page = PAGE;
  protected readonly modes: { mode: Mode; name: string }[] = [
    { mode: 'timeline', name: 'Zeitleiste' },
    { mode: 'map', name: 'Karte' },
    { mode: 'places', name: 'Orte' },
    { mode: 'trips', name: 'Reisen' },
    { mode: 'own', name: 'Eigene Alben' },
  ];
  protected readonly formatSize = formatSize;
  protected readonly downloadUrl = downloadUrl;

  constructor() {
    effect(() => this.load(this.kind()));
  }

  private load(kind: Kind): void {
    this.items.set(null);
    this.error.set(null);
    this.open.set(null);
    this.limit.set(PAGE);
    this.albumId.set(null);
    this.spot.set(null);
    this.mapView.set(null);
    this.selected.set(null);
    this.http.get<Media>('/api/media', { params: { kind } }).subscribe({
      next: (media) => this.items.set(media.items),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
    if (kind === 'photo') {
      this.http.get<{ albums: OwnAlbum[] }>('/api/albums').subscribe({
        next: (res) => this.own.set(res.albums),
        error: (err: unknown) => this.notice.set(errorMessage(err)),
      });
    }
  }

  /** Shows the photos up to the year and scrolls to it. */
  protected jump(year: Year): void {
    if (year.first >= this.limit()) {
      this.limit.set(year.first + PAGE);
    }
    // After Angular has drawn the photos added.
    setTimeout(() => document.getElementById(`year-${year.year}`)?.scrollIntoView());
  }

  protected setMode(mode: Mode): void {
    this.mode.set(mode);
    this.albumId.set(null);
    this.spot.set(null);
    this.limit.set(PAGE);
    this.renaming.set(false);
  }

  protected openAlbum(album: Album | null): void {
    this.albumId.set(album?.id ?? null);
    this.spot.set(null);
    this.search.set('');
    this.limit.set(PAGE);
    this.open.set(null);
    this.selected.set(null);
    this.renaming.set(false);
  }

  /** Shows the photos of a pin on the map. */
  protected openSpot(items: Placed[]): void {
    this.openAlbum(null);
    this.spot.set(spotAlbum(items));
  }

  /** "14.07.2026" or "01.07.2026 – 06.07.2026"; "leer" for an empty album. */
  protected span(album: Album): string {
    if (!album.from || !album.to) {
      return 'leer';
    }
    const from = date.format(album.from);
    const to = date.format(album.to);
    return from === to ? from : `${from} – ${to}`;
  }

  /** A click on a photo: opens it, or chooses it while choosing. */
  protected tile(index: number, item: Item): void {
    const selected = this.selected();
    if (!selected) {
      this.open.set(index);
      return;
    }
    const next = new Set(selected);
    if (!next.delete(item.path)) {
      next.add(item.path);
    }
    this.selected.set(next);
  }

  protected choose(on: boolean): void {
    this.selected.set(on ? new Set() : null);
    this.notice.set(null);
    if (on && !this.target()) {
      this.target.set(this.own()?.[0]?.id ?? '');
    }
  }

  /** Puts a changed own album from the server into the list. */
  private changed(album: OwnAlbum): void {
    this.own.update((own) => (own ?? []).map((a) => (a.id === album.id ? album : a)));
  }

  private failed(err: unknown): void {
    this.notice.set(errorMessage(err));
  }

  protected createAlbum(event: Event, name: string): void {
    event.preventDefault();
    if (!name.trim()) {
      return;
    }
    this.http.post<OwnAlbum>('/api/albums', { name }).subscribe({
      next: (album) => {
        this.own.update((own) => [...(own ?? []), album]);
        this.target.set(album.id);
        this.notice.set(`Album „${album.name}“ angelegt.`);
        (event.target as HTMLFormElement).reset();
      },
      error: (err: unknown) => this.failed(err),
    });
  }

  /** Adds the photos chosen to the target album. */
  protected addSelected(): void {
    const paths = [...(this.selected() ?? [])];
    const id = this.target();
    if (!paths.length || !id) {
      return;
    }
    this.http.post<OwnAlbum>('/api/albums/add', { id, paths }).subscribe({
      next: (album) => {
        this.changed(album);
        this.selected.set(new Set());
        this.notice.set(
          `${paths.length === 1 ? '1 Foto' : paths.length + ' Fotos'} zu „${album.name}“ hinzugefügt.`,
        );
      },
      error: (err: unknown) => this.failed(err),
    });
  }

  /** Takes the photos chosen out of the open own album; the files stay. */
  protected removeSelected(): void {
    const paths = [...(this.selected() ?? [])];
    const album = this.album();
    if (!paths.length || !album) {
      return;
    }
    this.http.post<OwnAlbum>('/api/albums/remove', { id: album.id, paths }).subscribe({
      next: (changed) => {
        this.changed(changed);
        this.selected.set(new Set());
        this.notice.set('Aus dem Album genommen; die Dateien bleiben.');
      },
      error: (err: unknown) => this.failed(err),
    });
  }

  protected renameAlbum(event: Event, name: string): void {
    event.preventDefault();
    const album = this.album();
    if (!album || !name.trim()) {
      return;
    }
    this.http.post<OwnAlbum>('/api/albums/rename', { id: album.id, name }).subscribe({
      next: (changed) => {
        this.changed(changed);
        this.renaming.set(false);
      },
      error: (err: unknown) => this.failed(err),
    });
  }

  protected deleteAlbum(): void {
    const album = this.album();
    if (!album || !confirm(`Album „${album.name}“ löschen? Die Fotos bleiben erhalten.`)) {
      return;
    }
    this.http.post('/api/albums/delete', { id: album.id }).subscribe({
      next: () => {
        this.own.update((own) => (own ?? []).filter((a) => a.id !== album.id));
        this.openAlbum(null);
        this.notice.set(`Album „${album.name}“ gelöscht.`);
      },
      error: (err: unknown) => this.failed(err),
    });
  }

  protected filter(value: string): void {
    this.search.set(value);
    this.limit.set(PAGE);
  }

  /** The thumbnail, or the photo itself when there is none or it failed. */
  protected tileUrl(item: Item): string {
    return item.thumb && !this.failedThumbs().has(item.path)
      ? thumbUrl(item.path)
      : viewUrl(item.path);
  }

  protected noThumb(item: Item): void {
    if (item.thumb && !this.failedThumbs().has(item.path)) {
      this.failedThumbs.update((failed) => new Set(failed).add(item.path));
    }
  }

  /** "Fotos/2026" for "/Fotos/2026/a.jpg". */
  protected folder(item: Item): string {
    return item.path.slice(1, -item.name.length - 1) || 'Start';
  }

  protected date(item: Item): string {
    return date.format(takenAt(item));
  }
}
