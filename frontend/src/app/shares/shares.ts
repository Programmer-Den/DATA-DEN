import { HttpClient } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { Grant, Grants, rights } from './grant';
import { Share, copy, shareLink } from './share';

const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium', timeStyle: 'short' });

/**
 * The links given out: copy them again or revoke them. Also the folders
 * shared with other accounts, to end.
 */
@Component({
  selector: 'app-shares',
  templateUrl: './shares.html',
  styleUrl: './shares.css',
})
export class SharesView {
  private readonly http = inject(HttpClient);

  protected readonly shares = signal<Share[] | null>(null);
  /** The folders I share with other accounts. */
  protected readonly grants = signal<Grant[] | null>(null);
  protected readonly rights = rights;
  protected readonly error = signal<string | null>(null);
  /** What the last action did, such as "Link kopiert". */
  protected readonly done = signal<string | null>(null);
  protected readonly busy = signal(false);

  constructor() {
    this.load();
    this.loadGrants();
  }

  private loadGrants(): void {
    this.http.get<Grants>('/api/grants').subscribe({
      next: (answer) => this.grants.set(answer.mine),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected endGrant(grant: Grant): void {
    if (!confirm(`„${grant.name}“ nicht mehr mit ${grant.to} teilen?`)) {
      return;
    }
    this.busy.set(true);
    this.error.set(null);
    this.done.set(null);
    this.http.post('/api/grants/delete', { id: grant.id }).subscribe({
      next: () => {
        this.busy.set(false);
        this.done.set(`„${grant.name}“ wird nicht mehr mit ${grant.to} geteilt`);
        this.loadGrants();
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.error.set(errorMessage(err));
      },
    });
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
