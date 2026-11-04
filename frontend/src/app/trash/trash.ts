import { HttpClient } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { formatSize } from '../format';

export interface TrashItem {
  id: string;
  /** Where it was, such as "/Fotos/a.jpg". */
  path: string;
  name: string;
  dir: boolean;
  size: number;
  deleted: string;
}

interface Trash {
  /** How long an item stays before it is gone for good. */
  days: number;
  items: TrashItem[];
}

const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium', timeStyle: 'short' });
const day = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium' });

/** The recycle bin: restore deleted files and folders, or delete them for good. */
@Component({
  selector: 'app-trash',
  templateUrl: './trash.html',
  styleUrl: './trash.css',
})
export class TrashView {
  private readonly http = inject(HttpClient);

  protected readonly trash = signal<Trash | null>(null);
  protected readonly error = signal<string | null>(null);
  /** What the last action did, such as "„a.jpg“ wiederhergestellt". */
  protected readonly done = signal<string | null>(null);
  protected readonly busy = signal(false);
  protected readonly formatSize = formatSize;

  constructor() {
    this.load();
  }

  private load(): void {
    this.http.get<Trash>('/api/trash').subscribe({
      next: (trash) => this.trash.set(trash),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  /** "Fotos/2026" for "/Fotos/2026/a.jpg". */
  protected folder(item: TrashItem): string {
    return item.path.slice(1, -item.name.length - 1) || 'Start';
  }

  protected deleted(item: TrashItem): string {
    return date.format(new Date(item.deleted));
  }

  /** The day the item is gone for good. */
  protected until(item: TrashItem, days: number): string {
    const until = new Date(item.deleted);
    until.setDate(until.getDate() + days);
    return day.format(until);
  }

  protected restore(item: TrashItem): void {
    this.act('/api/trash/restore', { id: item.id }, `„${item.name}“ wiederhergestellt`);
  }

  protected deleteForGood(item: TrashItem): void {
    if (confirm(`„${item.name}“ endgültig löschen? Das lässt sich nicht rückgängig machen.`)) {
      this.act('/api/trash/delete', { id: item.id }, `„${item.name}“ endgültig gelöscht`);
    }
  }

  protected empty(): void {
    if (confirm('Den Papierkorb leeren? Alles darin ist dann endgültig gelöscht.')) {
      this.act('/api/trash/empty', {}, 'Papierkorb geleert');
    }
  }

  private act(url: string, body: object, done: string): void {
    this.busy.set(true);
    this.error.set(null);
    this.done.set(null);
    this.http.post(url, body).subscribe({
      next: () => {
        this.busy.set(false);
        this.done.set(done);
        this.load();
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.error.set(errorMessage(err));
      },
    });
  }
}
