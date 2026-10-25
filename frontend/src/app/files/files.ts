import { HttpClient } from '@angular/common/http';
import { Component, computed, inject, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { UploadItem, Uploader, droppedJobs, pickedJobs, Job } from './uploader';

export interface Entry {
  name: string;
  dir: boolean;
  size: number;
  modified: string;
}

interface Listing {
  path: string;
  entries: Entry[];
}

/** One step of the path bar: its name and the folder it opens. */
interface Crumb {
  name: string;
  path: string;
}

const sizeUnits = ['B', 'KB', 'MB', 'GB', 'TB'];
const number = new Intl.NumberFormat('de-DE', { maximumFractionDigits: 1 });
const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium', timeStyle: 'short' });

/** "1,5 MB" for 1572864. */
export function formatSize(bytes: number): string {
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < sizeUnits.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${number.format(value)} ${sizeUnits[unit]}`;
}

/** The folders of the data directory, with downloads of their files. */
@Component({
  selector: 'app-files',
  templateUrl: './files.html',
  styleUrl: './files.css',
})
export class Files {
  private readonly http = inject(HttpClient);

  /** The folder shown, "/" for the top. */
  protected readonly path = signal('/');
  protected readonly entries = signal<Entry[] | null>(null);
  protected readonly error = signal<string | null>(null);
  protected readonly crumbs = computed<Crumb[]>(() => {
    const parts = this.path().split('/').filter(Boolean);
    return [
      { name: 'Start', path: '/' },
      ...parts.map((name, i) => ({ name, path: '/' + parts.slice(0, i + 1).join('/') })),
    ];
  });
  protected readonly formatSize = formatSize;
  protected readonly uploader = inject(Uploader);
  /** True while files are dragged over the page. */
  protected readonly dragging = signal(false);

  constructor() {
    this.open('/');
  }

  protected open(path: string): void {
    this.error.set(null);
    this.http.get<Listing>('/api/files', { params: { path } }).subscribe({
      next: (listing) => {
        this.path.set(listing.path);
        this.entries.set(listing.entries);
      },
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected child(entry: Entry): string {
    return (this.path() === '/' ? '/' : this.path() + '/') + entry.name;
  }

  protected downloadUrl(entry: Entry): string {
    return '/api/download?path=' + encodeURIComponent(this.child(entry));
  }

  protected modified(entry: Entry): string {
    return date.format(new Date(entry.modified));
  }

  protected percent(item: UploadItem): number {
    return item.size === 0 ? 100 : Math.floor((item.loaded / item.size) * 100);
  }

  protected pick(event: Event): void {
    const input = event.target as HTMLInputElement;
    if (input.files?.length) {
      void this.start(pickedJobs(input.files, this.path()));
    }
    // The same files can be picked again after an error.
    input.value = '';
  }

  protected dragOver(event: DragEvent): void {
    event.preventDefault();
    this.dragging.set(true);
  }

  protected async drop(event: DragEvent): Promise<void> {
    event.preventDefault();
    this.dragging.set(false);
    if (event.dataTransfer) {
      // Read before the first await: the browser empties the DataTransfer
      // once the drop handler returns.
      const jobs = droppedJobs(event.dataTransfer, this.path());
      await this.start(await jobs);
    }
  }

  private async start(jobs: Job[]): Promise<void> {
    const folder = this.path();
    await this.uploader.upload(jobs);
    // Shows the new files if the folder is still open.
    if (this.path() === folder) {
      this.open(folder);
    }
  }
}
