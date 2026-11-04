import { HttpClient } from '@angular/common/http';
import { Component, computed, effect, inject, input, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { formatSize } from '../format';
import { Album, placeAlbums, tripAlbums } from './albums';
import { Item, Kind, downloadUrl, takenAt, thumbUrl, viewUrl } from './item';
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

/** How the photos are shown: all by month, or as albums by place or trip. */
export type Mode = 'timeline' | 'places' | 'trips';

/** Files shown at first and added by "Mehr anzeigen", so thousands of photos stay quick. */
export const PAGE = 120;

const monthName = new Intl.DateTimeFormat('de-DE', { month: 'long', year: 'numeric' });
const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium' });

/**
 * Every photo, video or document of the data directory, newest first: photos
 * as a timeline by month taken with a list of years to jump to, or as albums
 * by place and by trip; videos as tiles, documents as a list. A click opens
 * the viewer.
 */
@Component({
  selector: 'app-media',
  imports: [Viewer],
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
  /** The album open, whose photos are shown; null shows the timeline or the albums. */
  protected readonly album = signal<Album | null>(null);

  /** Every file, or the photos of the album open. */
  private readonly all = computed(() => this.album()?.items ?? this.items() ?? []);
  protected readonly found = computed(() => {
    const search = this.search().trim().toLowerCase();
    const items = this.all();
    return search ? items.filter((item) => item.path.toLowerCase().includes(search)) : items;
  });
  /** The albums of the mode chosen, found by name or by a photo's path. */
  protected readonly albums = computed<Album[]>(() => {
    const items = this.items() ?? [];
    const mode = this.mode();
    const albums =
      mode === 'places' ? placeAlbums(items) : mode === 'trips' ? tripAlbums(items) : [];
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
    () => this.kind() === 'photo' && this.mode() !== 'timeline' && this.album() === null,
  );
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
    { mode: 'places', name: 'Orte' },
    { mode: 'trips', name: 'Reisen' },
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
    this.album.set(null);
    this.http.get<Media>('/api/media', { params: { kind } }).subscribe({
      next: (media) => this.items.set(media.items),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
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
    this.album.set(null);
    this.limit.set(PAGE);
  }

  protected openAlbum(album: Album | null): void {
    this.album.set(album);
    this.search.set('');
    this.limit.set(PAGE);
    this.open.set(null);
  }

  /** "14.07.2026" or "01.07.2026 – 06.07.2026". */
  protected span(album: Album): string {
    const from = date.format(album.from);
    const to = date.format(album.to);
    return from === to ? from : `${from} – ${to}`;
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
