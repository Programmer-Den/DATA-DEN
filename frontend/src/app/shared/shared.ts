import { HttpClient } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { Files } from '../files/files';
import { Grant, Grants, rights } from '../shares/grant';

/** The folders other accounts share with me: open one or leave it. */
@Component({
  selector: 'app-shared',
  imports: [Files],
  templateUrl: './shared.html',
  styleUrl: './shared.css',
})
export class Shared {
  private readonly http = inject(HttpClient);

  protected readonly grants = signal<Grant[] | null>(null);
  protected readonly error = signal<string | null>(null);
  protected readonly busy = signal(false);
  /** The folder open, or null for the list. */
  protected readonly opened = signal<Grant | null>(null);
  protected readonly rights = rights;

  constructor() {
    this.load();
  }

  private load(): void {
    this.http.get<Grants>('/api/grants').subscribe({
      next: (answer) => this.grants.set(answer.withMe),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected leave(grant: Grant): void {
    if (
      !confirm(
        `„${grant.name}“ von ${grant.owner} verlassen? Nur ${grant.owner} kann es wieder teilen.`,
      )
    ) {
      return;
    }
    this.busy.set(true);
    this.error.set(null);
    this.http.post('/api/grants/delete', { id: grant.id }).subscribe({
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
