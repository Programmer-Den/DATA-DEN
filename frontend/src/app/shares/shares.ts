import { HttpClient } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { Share, copy, shareLink } from './share';

const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium', timeStyle: 'short' });

/** The links given out: copy them again or revoke them. */
@Component({
  selector: 'app-shares',
  templateUrl: './shares.html',
  styleUrl: './shares.css',
})
export class SharesView {
  private readonly http = inject(HttpClient);

  protected readonly shares = signal<Share[] | null>(null);
  protected readonly error = signal<string | null>(null);
  /** What the last action did, such as "Link kopiert". */
  protected readonly done = signal<string | null>(null);
  protected readonly busy = signal(false);

  constructor() {
    this.load();
  }

  private load(): void {
    this.http.get<{ items: Share[] }>('/api/shares').subscribe({
      next: (answer) => this.shares.set(answer.items),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected date(when: string): string {
    return date.format(new Date(when));
  }

  protected async copy(share: Share): Promise<void> {
    this.error.set(null);
    if (await copy(shareLink(share.id))) {
      this.done.set(`Link zu „${share.name}“ kopiert`);
    } else {
      this.error.set('Kopieren nicht erlaubt: ' + shareLink(share.id));
    }
  }

  protected revoke(share: Share): void {
    if (!confirm(`Den Link zu „${share.name}“ widerrufen? Er funktioniert dann nicht mehr.`)) {
      return;
    }
    this.busy.set(true);
    this.error.set(null);
    this.done.set(null);
    this.http.post('/api/shares/delete', { id: share.id }).subscribe({
      next: () => {
        this.busy.set(false);
        this.done.set(`Link zu „${share.name}“ widerrufen`);
        this.load();
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.error.set(errorMessage(err));
      },
    });
  }
}
