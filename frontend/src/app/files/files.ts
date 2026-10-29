import { HttpClient } from '@angular/common/http';
import { Component, computed, inject, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { formatSize } from '../format';
import { Item, Kind, downloadUrl } from '../media/item';
import { Viewer } from '../media/viewer';
import { UploadItem, Uploader, droppedJobs, pickedJobs, Job } from './uploader';

export interface Entry {
  name: string;
  dir: boolean;
  size: number;
  modified: string;
  kind?: Kind;
  preview?: boolean;
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

const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium', timeStyle: 'short' });

/** The folders of the data directory: open, download and upload files. */
@Component({
  selector: 'app-files',
  imports: [Viewer],
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
  /** The files of the folder, for the viewer to go through. */
  protected readonly files = computed<Item[]>(() =>
    (this.entries() ?? [])
      .filter((entry) => !entry.dir)
      .map((entry) => ({ ...entry, path: this.child(entry) })),
  );
  /** The file open in the viewer, an index into files, or null. */
  protected readonly viewing = signal<number | null>(null);
  protected readonly formatSize = formatSize;
  protected readonly uploader = inject(Uploader);
  /** True while files are dragged over the page. */
  protected readonly dragging = signal(false);
  /** True while the name of a new folder is asked for. */
  protected readonly creating = signal(false);
  /** The entry being renamed, by name, or null. */
  protected readonly renaming = signal<string | null>(null);
  /** The file or folder being moved: its path stays while other folders open. */
  protected readonly moving = signal<{ path: string; name: string } | null>(null);
  protected readonly busy = signal(false);

  constructor() {
    this.open('/');
  }

  protected open(path: string): void {
    this.error.set(null);
    this.viewing.set(null);
    this.renaming.set(null);
    this.http.get<Listing>('/api/files', { params: { path } }).subscribe({
      next: (listing) => {
        this.path.set(listing.path);
        this.entries.set(listing.entries);
      },
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected child(entry: Entry): string {
    return this.childPath(entry.name);
  }

  protected downloadUrl(entry: Entry): string {
    return downloadUrl(this.child(entry));
  }

  protected view(entry: Entry): void {
    this.viewing.set(this.files().findIndex((file) => file.name === entry.name));
  }

  protected createFolder(event: Event, name: string): void {
    event.preventDefault();
    name = name.trim();
    if (!name) {
      return;
    }
    this.change('/api/folders', { path: this.childPath(name) }, () => this.creating.set(false));
  }

  protected rename(event: Event, entry: Entry, name: string): void {
    event.preventDefault();
    name = name.trim();
    if (!name || name === entry.name) {
      this.renaming.set(null);
      return;
    }
    this.change('/api/move', { from: this.child(entry), to: this.childPath(name) }, () =>
      this.renaming.set(null),
    );
  }

  protected startMove(entry: Entry): void {
    this.moving.set({ path: this.child(entry), name: entry.name });
  }

  /** Moves the file or folder picked with startMove into the open folder. */
  protected moveHere(): void {
    const moving = this.moving();
    if (moving) {
      this.change('/api/move', { from: moving.path, to: this.childPath(moving.name) }, () =>
        this.moving.set(null),
      );
    }
  }

  protected remove(entry: Entry): void {
    const what = entry.dir
      ? `Den Ordner „${entry.name}“ mit seinem ganzen Inhalt endgültig löschen?`
      : `„${entry.name}“ endgültig löschen?`;
    if (confirm(what)) {
      this.change('/api/delete', { path: this.child(entry) });
    }
  }

  /** Sends a change to the server and shows the folder again afterwards. */
  private change(url: string, body: object, done?: () => void): void {
    this.busy.set(true);
    this.error.set(null);
    this.http.post(url, body).subscribe({
      next: () => {
        this.busy.set(false);
        done?.();
        this.open(this.path());
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.error.set(errorMessage(err));
      },
    });
  }

  private childPath(name: string): string {
    return (this.path() === '/' ? '/' : this.path() + '/') + name;
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
