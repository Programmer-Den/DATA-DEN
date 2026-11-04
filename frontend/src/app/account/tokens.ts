import { HttpClient } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { errorMessage } from '../auth.service';

export interface AppPassword {
  id: string;
  name: string;
  readOnly: boolean;
  created: string;
  /** null lasts until it is revoked. */
  expires: string | null;
  lastUsed: string | null;
}

/** A date as "4.11.2026", or "–" for none. */
export function formatDay(iso: string | null): string {
  return iso ? new Date(iso).toLocaleDateString('de-DE') : '–';
}

/**
 * App passwords for scripts, apps and WebDAV: they work instead of the
 * password and the second factor, and each one can be revoked.
 */
@Component({
  selector: 'app-tokens',
  templateUrl: './tokens.html',
  styleUrl: './account.css',
})
export class TokensView {
  private readonly http = inject(HttpClient);

  protected readonly tokens = signal<AppPassword[] | null>(null);
  /** A new token, shown once right after it was created. */
  protected readonly created = signal<{ name: string; token: string } | null>(null);
  protected readonly error = signal<string | null>(null);
  protected readonly busy = signal(false);
  protected readonly formatDay = formatDay;

  constructor() {
    this.load();
  }

  private load(): void {
    this.http.get<{ tokens: AppPassword[] }>('/api/account/tokens').subscribe({
      next: (answer) => this.tokens.set(answer.tokens),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected create(event: Event, form: HTMLFormElement): void {
    event.preventDefault();
    const data = new FormData(form);
    const days = String(data.get('expiresDays') ?? '').trim();
    const body = {
      name: String(data.get('name') ?? '').trim(),
      password: String(data.get('password') ?? ''),
      readOnly: data.get('readOnly') === 'on',
      expiresDays: days === '' ? 0 : Number(days),
    };
    this.busy.set(true);
    this.error.set(null);
    this.created.set(null);
    this.http.post<{ token: string; info: AppPassword }>('/api/account/tokens', body).subscribe({
      next: (answer) => {
        this.busy.set(false);
        form.reset();
        this.created.set({ name: answer.info.name, token: answer.token });
        this.load();
      },
      error: (err: unknown) => this.failed(err),
    });
  }

  protected revoke(token: AppPassword): void {
    if (
      !confirm(
        `Das App-Passwort „${token.name}“ widerrufen? Was es nutzt, kommt dann nicht mehr rein.`,
      )
    ) {
      return;
    }
    this.busy.set(true);
    this.error.set(null);
    this.http.post(`/api/account/tokens/${encodeURIComponent(token.id)}/delete`, {}).subscribe({
      next: () => {
        this.busy.set(false);
        if (this.created()?.name === token.name) {
          this.created.set(null);
        }
        this.load();
      },
      error: (err: unknown) => this.failed(err),
    });
  }

  private failed(err: unknown): void {
    this.busy.set(false);
    this.error.set(errorMessage(err));
  }
}
