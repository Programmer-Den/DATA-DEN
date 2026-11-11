import { NgTemplateOutlet } from '@angular/common';
import { HttpClient } from '@angular/common/http';
import { Component, computed, effect, inject, input, output, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { formatSize } from '../format';
import { Item, downloadUrl } from '../media/item';
import { Viewer } from '../media/viewer';

/** A favourite or recently used file or folder. */
export interface Marked extends Item {
  dir: boolean;
  /** When it was last used, only in the recent list. */
  used?: string;
}

export interface TagCount {
  name: string;
  count: number;
}

interface Marks {
  tags: TagCount[];
  favourites: Marked[];
  recent: Marked[];
}

interface Tagged {
  tag: string;
  items: Marked[];
}

const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium', timeStyle: 'short' });

/** Favourites, recently used files, and the files of a tag. */
@Component({
  selector: 'app-marks',
  imports: [NgTemplateOutlet, Viewer],
  templateUrl: './marks.html',
  styleUrl: './marks.css',
})
export class MarksView {
  private readonly http = inject(HttpClient);

  /** The tag to show at first, or empty for none. */
  readonly tag = input('');
  /** Asks to show a folder in the folder view. */
  readonly openFolder = output<string>();

  protected readonly marks = signal<Marks | null>(null);
  /** The files of the chosen tag, or null when none is chosen. */
  protected readonly tagged = signal<Tagged | null>(null);
  protected readonly error = signal<string | null>(null);
  protected readonly busy = signal(false);
  /** The files the viewer goes through: those of the list a file was opened from. */
  protected readonly previews = signal<Marked[]>([]);
  /** The file open in the viewer, an index into previews, or null. */
  protected readonly viewing = signal<number | null>(null);
  protected readonly formatSize = formatSize;
  protected readonly downloadUrl = downloadUrl;
  protected readonly empty = computed(() => {
    const marks = this.marks();
    return !!marks && !marks.tags.length && !marks.favourites.length && !marks.recent.length;
  });

  constructor() {
    this.load();
    effect(() => {
      const tag = this.tag();
      if (tag) {
        this.showTag(tag);
      }
    });
  }

  private load(): void {
    this.http.get<Marks>('/api/marks').subscribe({
      next: (marks) => this.marks.set(marks),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected showTag(tag: string): void {
    this.error.set(null);
    this.http.get<Tagged>('/api/tagged', { params: { tag } }).subscribe({
      next: (tagged) => this.tagged.set(tagged),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected closeTag(): void {
    this.tagged.set(null);
  }

  /** "/Fotos/2026" for "/Fotos/2026/a.jpg". */
  protected folder(item: Marked): string {
    return item.path.slice(0, -item.name.length - 1) || '/';
  }

  protected used(item: Marked): string {
    return item.used ? date.format(new Date(item.used)) : '';
  }

  /** Opens a folder in the folder view, and a file with a preview in the viewer. */
  protected open(item: Marked, list: Marked[]): void {
    if (item.dir) {
      this.openFolder.emit(item.path);
      return;
    }
    const previews = list.filter((other) => !other.dir && other.preview);
    this.previews.set(previews);
    this.viewing.set(previews.indexOf(item));
  }

  protected unfavourite(item: Marked): void {
    this.busy.set(true);
    this.error.set(null);
    this.http.post('/api/favourites', { path: item.path, favourite: false }).subscribe({
      next: () => {
        this.busy.set(false);
        this.load();
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.error.set(errorMessage(err));
      },
    });
  }
}
