import { HttpClient } from '@angular/common/http';
import { Component, DestroyRef, inject, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { formatSize } from '../format';

/** A file whose content no longer matches its checksum. */
export interface Damaged {
  path: string;
  size: number;
  expected: string;
  actual: string;
  found: string;
}

export interface Integrity {
  /** How many files have a checksum. */
  files: number;
  running: boolean;
  done: number;
  total: number;
  lastCheck?: string;
  /** How often every file is read again. */
  verifyDays: number;
  damaged: Damaged[];
}

/** Files with the same content. */
export interface Duplicate {
  sha256: string;
  size: number;
  paths: string[];
}

export interface Duplicates {
  groups: Duplicate[];
  /** Bytes to gain by keeping one file of each group. */
  wasted: number;
}

/** How often the page asks again while a check runs. */
export const POLL_MS = 3000;

const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium', timeStyle: 'short' });

/** The checks: damaged files and duplicates, found by their checksums. */
@Component({
  selector: 'app-checks',
  templateUrl: './checks.html',
  styleUrl: './checks.css',
})
export class ChecksView {
  private readonly http = inject(HttpClient);

  protected readonly integrity = signal<Integrity | null>(null);
  protected readonly duplicates = signal<Duplicates | null>(null);
  protected readonly error = signal<string | null>(null);
  /** What the last action did. */
  protected readonly done = signal<string | null>(null);
  protected readonly busy = signal(false);
  protected readonly formatSize = formatSize;
  private poll?: ReturnType<typeof setTimeout>;

  constructor() {
    this.load();
    inject(DestroyRef).onDestroy(() => clearTimeout(this.poll));
  }

  private load(): void {
    clearTimeout(this.poll);
    this.http.get<Integrity>('/api/integrity').subscribe({
      next: (state) => {
        this.integrity.set(state);
        if (state.running) {
          this.poll = setTimeout(() => this.load(), POLL_MS);
        }
      },
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
    this.http.get<Duplicates>('/api/duplicates').subscribe({
      next: (duplicates) => this.duplicates.set(duplicates),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected date(value: string): string {
    return date.format(new Date(value));
  }

  protected percent(state: Integrity): number {
    return state.total ? Math.floor((state.done / state.total) * 100) : 0;
  }

  protected checkAll(): void {
    this.act('/api/integrity/check', {}, 'Prüfung aller Dateien gestartet', () =>
      setTimeout(() => this.load(), 500),
    );
  }

  protected accept(file: Damaged): void {
    if (
      confirm(
        `„${file.path}“ so übernehmen, wie die Datei jetzt ist? Nur tun, wenn sie geprüft oder aus einer Kopie wiederhergestellt ist.`,
      )
    ) {
      this.act('/api/integrity/accept', { path: file.path }, `„${file.path}“ übernommen`);
    }
  }

  protected remove(path: string): void {
    this.act('/api/delete', { path }, `„${path}“ in den Papierkorb verschoben`);
  }

  private act(url: string, body: object, done: string, after = () => this.load()): void {
    this.busy.set(true);
    this.error.set(null);
    this.done.set(null);
    this.http.post(url, body).subscribe({
      next: () => {
        this.busy.set(false);
        this.done.set(done);
        after();
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.error.set(errorMessage(err));
      },
    });
  }
}
