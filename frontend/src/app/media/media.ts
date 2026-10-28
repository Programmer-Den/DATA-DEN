import { HttpClient } from '@angular/common/http';
import { Component, computed, effect, inject, input, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { formatSize } from '../format';
import { Item, Kind, downloadUrl, viewUrl } from './item';
import { Viewer } from './viewer';

interface Media {
  kind: Kind;
  items: Item[];
}

/** Photos of one month, such as "Oktober 2026". */
interface Month {
  name: string;
  items: { item: Item; index: number }[];
}

/** Files shown at first and added by "Mehr anzeigen", so thousands of photos stay quick. */
export const PAGE = 120;

const monthName = new Intl.DateTimeFormat('de-DE', { month: 'long', year: 'numeric' });
const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium' });

/**
 * Every photo, video or document of the data directory, newest first: photos
 * as a gallery by month, videos as tiles, documents as a list. A click opens
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

  protected readonly found = computed(() => {
    const search = this.search().trim().toLowerCase();
    const items = this.items() ?? [];
    return search ? items.filter((item) => item.path.toLowerCase().includes(search)) : items;
  });
  protected readonly shown = computed(() => this.found().slice(0, this.limit()));
  protected readonly months = computed<Month[]>(() => {
    const months: Month[] = [];
    this.shown().forEach((item, index) => {
      const name = monthName.format(new Date(item.modified));
      if (months.at(-1)?.name !== name) {
        months.push({ name, items: [] });
      }
      months.at(-1)!.items.push({ item, index });
    });
    return months;
  });

  protected readonly page = PAGE;
  protected readonly formatSize = formatSize;
  protected readonly viewUrl = viewUrl;
  protected readonly downloadUrl = downloadUrl;

  constructor() {
    effect(() => this.load(this.kind()));
  }

  private load(kind: Kind): void {
    this.items.set(null);
    this.error.set(null);
    this.open.set(null);
    this.limit.set(PAGE);
    this.http.get<Media>('/api/media', { params: { kind } }).subscribe({
      next: (media) => this.items.set(media.items),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected filter(value: string): void {
    this.search.set(value);
    this.limit.set(PAGE);
  }

  /** "Fotos/2026" for "/Fotos/2026/a.jpg". */
  protected folder(item: Item): string {
    return item.path.slice(1, -item.name.length - 1) || 'Start';
  }

  protected date(item: Item): string {
    return date.format(new Date(item.modified));
  }
}
