import { HttpClient } from '@angular/common/http';
import { Component, OnInit, inject, input, output, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { formatSize } from '../format';

/** An older content of a file, kept when it was replaced. */
export interface Version {
  id: string;
  path: string;
  size: number;
  /** When this content was written. */
  modified: string;
  /** When it was replaced. */
  saved: string;
}

interface VersionList {
  days: number;
  versions: Version[];
}

const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium', timeStyle: 'short' });

/** The older versions of a file: download, restore or delete one. */
@Component({
  selector: 'app-versions',
  templateUrl: './versions.html',
  styleUrl: './files.css',
})
export class Versions implements OnInit {
  private readonly http = inject(HttpClient);

  /** The file, such as "/Texte/brief.txt". */
  readonly path = input.required<string>();
  readonly name = input.required<string>();
  readonly closed = output<void>();
  /** A version became the file again. */
  readonly restored = output<void>();

  protected readonly list = signal<VersionList | null>(null);
  protected readonly busy = signal(false);
  protected readonly error = signal<string | null>(null);
  protected readonly formatSize = formatSize;

  ngOnInit(): void {
    this.load();
  }

  protected load(): void {
    this.http.get<VersionList>('/api/versions', { params: { path: this.path() } }).subscribe({
      next: (list) => this.list.set(list),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected downloadUrl(version: Version): string {
    return (
      '/api/versions/download?path=' +
      encodeURIComponent(this.path()) +
      '&id=' +
      encodeURIComponent(version.id)
    );
  }

  protected format(time: string): string {
    return date.format(new Date(time));
  }

  protected restore(version: Version): void {
    this.change('/api/versions/restore', version, () => this.restored.emit());
  }

  protected remove(version: Version): void {
    this.change('/api/versions/delete', version);
  }

  private change(url: string, version: Version, done?: () => void): void {
    this.busy.set(true);
    this.error.set(null);
    this.http.post(url, { path: this.path(), id: version.id }).subscribe({
      next: () => {
        this.busy.set(false);
        done?.();
        this.load();
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.error.set(errorMessage(err));
      },
    });
  }
}
