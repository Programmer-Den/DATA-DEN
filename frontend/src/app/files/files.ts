import { HttpClient } from '@angular/common/http';
import { Component, OnInit, computed, inject, input, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { formatSize } from '../format';
import { Item, Kind, downloadUrl } from '../media/item';
import { Viewer } from '../media/viewer';
import { Grant } from '../shares/grant';
import { GrantForm } from '../shares/grant-form';
import { Editor } from '../editor/editor';
import { OfficeFormats } from '../office/formats';
import { OfficeFrame } from '../office/office-frame';
import { ShareForm } from '../shares/share-form';
import { StartFolder } from './start-folder';
import { UploadItem, Uploader, droppedJobs, pickedJobs, Job } from './uploader';
import { Versions } from './versions';

export interface Entry {
  name: string;
  dir: boolean;
  size: number;
  modified: string;
  kind?: Kind;
  preview?: boolean;
  text?: boolean;
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

/**
 * The folders of the data directory: open, download and upload files. With
 * a grant, the same for a folder another account shares.
 */
@Component({
  selector: 'app-files',
  imports: [Viewer, Editor, OfficeFrame, ShareForm, GrantForm, Versions],
  templateUrl: './files.html',
  styleUrl: './files.css',
})
export class Files implements OnInit {
  private readonly http = inject(HttpClient);
  private readonly startFolder = inject(StartFolder);

  /** The folder of another account to show instead of my own space. */
  readonly grant = input<Grant | null>(null);
  /** True when the folders may be changed: always in my own space. */
  protected readonly canWrite = computed(() => this.grant()?.write ?? true);

  /** The folder shown, "/" for the top. */
  protected readonly path = signal('/');
  protected readonly entries = signal<Entry[] | null>(null);
  protected readonly error = signal<string | null>(null);
  protected readonly crumbs = computed<Crumb[]>(() => {
    const parts = this.path().split('/').filter(Boolean);
    return [
      { name: this.grant()?.name ?? 'Start', path: '/' },
      ...parts.map((name, i) => ({ name, path: '/' + parts.slice(0, i + 1).join('/') })),
    ];
  });
  /** The files of the folder, for the viewer to go through. */
  protected readonly files = computed<Item[]>(() =>
    (this.entries() ?? [])
      .filter((entry) => !entry.dir)
      .map((entry) => ({ ...entry, path: this.child(entry), grant: this.grant()?.id })),
  );
  /** The file open in the viewer, an index into files, or null. */
  protected readonly viewing = signal<number | null>(null);
  protected readonly formatSize = formatSize;
  protected readonly uploader = inject(Uploader);
  /** True while files are dragged over the page. */
  protected readonly dragging = signal(false);
  /** True while the name of a new folder is asked for. */
  protected readonly creating = signal(false);
  /** True while the name of a new text file is asked for. */
  protected readonly writing = signal(false);
  /** The new text file open in the editor, or null. */
  protected readonly editing = signal<string | null>(null);
  /** The office document open in Collabora, or null. */
  protected readonly officeEditing = signal<string | null>(null);
  protected readonly office = inject(OfficeFormats);
  /** True when the viewer saved a file, so the folder is loaded again. */
  private changed = false;
  /** The entry being renamed, by name, or null. */
  protected readonly renaming = signal<string | null>(null);
  /** The file or folder being moved: its path stays while other folders open. */
  protected readonly moving = signal<{ path: string; name: string } | null>(null);
  protected readonly busy = signal(false);
  /** The file or folder a link is being made for, or null. */
  protected readonly sharing = signal<{ path: string; name: string; dir: boolean } | null>(null);
  /** The folder being shared with another account, or null. */
  protected readonly granting = signal<{ path: string; name: string } | null>(null);
  /** The file whose older versions are shown, or null. */
  protected readonly versions = signal<{ path: string; name: string } | null>(null);
  /** True while uploads wait for the answer whether to replace existing files. */
  protected readonly asking = computed(() =>
    this.uploader.items().some((item) => item.status === 'existiert schon'),
  );
  /** What was just moved to the recycle bin, for undo. */
  protected readonly deleted = signal<{ id: string; name: string } | null>(null);

  ngOnInit(): void {
    // A folder picked elsewhere is one of my own space.
    this.open(this.grant() ? '/' : this.startFolder.take());
  }

  /** The query that puts a request into the shared folder, if any. */
  private get grantQuery(): string {
    const grant = this.grant();
    return grant ? '?grant=' + grant.id : '';
  }

  protected open(path: string): void {
    this.error.set(null);
    this.viewing.set(null);
    this.renaming.set(null);
    const grant = this.grant();
    const params: Record<string, string> = grant ? { path, grant: grant.id } : { path };
    this.http.get<Listing>('/api/files', { params }).subscribe({
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
    return downloadUrl(this.child(entry), this.grant()?.id);
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

  /** Makes an empty text file and opens it in the editor. */
  protected createText(event: Event, name: string): void {
    event.preventDefault();
    name = textName(name);
    if (!name) {
      return;
    }
    const path = this.childPath(name);
    this.busy.set(true);
    this.error.set(null);
    this.http.put('/api/text', { path, content: '', version: '' }).subscribe({
      next: () => {
        this.busy.set(false);
        this.writing.set(false);
        this.open(this.path());
        this.editing.set(path);
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.error.set(errorMessage(err));
      },
    });
  }

  protected editOffice(entry: Entry): void {
    this.officeEditing.set(this.child(entry));
  }

  protected closeOffice(): void {
    this.officeEditing.set(null);
    this.open(this.path());
  }

  protected closeEditor(): void {
    this.editing.set(null);
    this.open(this.path());
  }

  protected closeViewer(): void {
    this.viewing.set(null);
    if (this.changed) {
      this.changed = false;
      this.open(this.path());
    }
  }

  protected viewerChanged(): void {
    this.changed = true;
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

  protected share(entry: Entry): void {
    this.sharing.set({ path: this.child(entry), name: entry.name, dir: entry.dir });
  }

  protected shareWithAccount(entry: Entry): void {
    this.granting.set({ path: this.child(entry), name: entry.name });
  }

  protected showVersions(entry: Entry): void {
    this.versions.set({ path: this.child(entry), name: entry.name });
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

  /** Moves to the recycle bin; the bar that follows can undo it. */
  protected remove(entry: Entry): void {
    // In another account's folder, only its owner can restore from the
    // recycle bin; there is no id then.
    this.change<{ id?: string }>('/api/delete', { path: this.child(entry) }, (answer) => {
      if (answer.id) {
        this.deleted.set({ id: answer.id, name: entry.name });
      }
    });
  }

  protected undoDelete(): void {
    const deleted = this.deleted();
    if (deleted) {
      this.change('/api/trash/restore', { id: deleted.id }, () => this.deleted.set(null));
    }
  }

  /** Sends a change to the server and shows the folder again afterwards. */
  private change<T = unknown>(url: string, body: object, done?: (answer: T) => void): void {
    this.busy.set(true);
    this.error.set(null);
    this.deleted.set(null);
    this.http.post<T>(url + this.grantQuery, body).subscribe({
      next: (answer) => {
        this.busy.set(false);
        done?.(answer);
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

  /** Replaces existing files with the uploads that met them, or all of them. */
  protected replace(ids?: number[]): Promise<void> {
    return this.refreshAfter(this.uploader.replace(ids));
  }

  private start(jobs: Job[]): Promise<void> {
    const grant = this.grant()?.id;
    return this.refreshAfter(
      this.uploader.upload(grant ? jobs.map((job) => ({ ...job, grant })) : jobs),
    );
  }

  private async refreshAfter(uploads: Promise<void>): Promise<void> {
    const folder = this.path();
    await uploads;
    // Shows the new files if the folder is still open.
    if (this.path() === folder) {
      this.open(folder);
    }
  }
}

/** The extensions the editor opens. */
const TEXT_EXTENSIONS = ['.txt', '.md', '.csv', '.log'];

/** The name of a new text file: Markdown unless it names a text extension. */
export function textName(name: string): string {
  name = name.trim();
  if (!name) {
    return '';
  }
  const lower = name.toLowerCase();
  return TEXT_EXTENSIONS.some((ext) => lower.endsWith(ext)) ? name : name + '.md';
}
