import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { AuthService, errorMessage } from '../auth.service';
import { createPasskey, passkeyError, passkeysSupported } from '../passkey';
import { formatDay } from './tokens';

export interface PasskeyInfo {
  id: string;
  name: string;
  created: string;
  lastUsed: string | null;
}

/**
 * Passkeys: log in with the phone's or computer's lock (fingerprint, face or
 * PIN) instead of password and code. They only work on DATA-DEN's own
 * address, so a fake login page gets nothing.
 */
@Component({
  selector: 'app-passkeys',
  templateUrl: './passkeys.html',
  styleUrl: './account.css',
})
export class PasskeysView {
  private readonly http = inject(HttpClient);
  protected readonly auth = inject(AuthService);

  /** null until the server answered. */
  protected readonly passkeys = signal<PasskeyInfo[] | null>(null);
  /** Whether the server has passkeys set up (DATA_DEN_ORIGIN). */
  protected readonly available = signal(false);
  protected readonly supported = passkeysSupported();
  protected readonly added = signal<string | null>(null);
  protected readonly error = signal<string | null>(null);
  protected readonly busy = signal(false);
  protected readonly formatDay = formatDay;

  constructor() {
    this.load();
  }

  private load(): void {
    this.http
      .get<{ available: boolean; passkeys: PasskeyInfo[] }>('/api/account/passkeys')
      .subscribe({
        next: (answer) => {
          this.available.set(answer.available);
          this.passkeys.set(answer.passkeys);
        },
        error: (err: unknown) => this.error.set(errorMessage(err)),
      });
  }

  protected async add(event: Event, form: HTMLFormElement): Promise<void> {
    event.preventDefault();
    const data = new FormData(form);
    const name = String(data.get('name') ?? '').trim();
    const confirmation = {
      password: String(data.get('password') ?? ''),
      code: String(data.get('code') ?? '').trim(),
    };
    this.busy.set(true);
    this.error.set(null);
    this.added.set(null);
    try {
      const begin = await new Promise<{ publicKey: Record<string, unknown> }>((resolve, reject) =>
        this.http
          .post<{ publicKey: Record<string, unknown> }>('/api/account/passkeys/begin', confirmation)
          .subscribe({ next: resolve, error: reject }),
      );
      const credential = await createPasskey(begin.publicKey);
      await new Promise<PasskeyInfo>((resolve, reject) =>
        this.http
          .post<PasskeyInfo>('/api/account/passkeys', { name, credential })
          .subscribe({ next: resolve, error: reject }),
      );
      form.reset();
      this.added.set(name);
      this.load();
    } catch (err: unknown) {
      this.error.set(err instanceof HttpErrorResponse ? errorMessage(err) : passkeyError(err));
    } finally {
      this.busy.set(false);
    }
  }

  protected remove(passkey: PasskeyInfo): void {
    if (
      !confirm(
        `Den Passkey „${passkey.name}“ entfernen? Auf dem Gerät bleibt er gespeichert, meldet aber nicht mehr an.`,
      )
    ) {
      return;
    }
    this.busy.set(true);
    this.error.set(null);
    this.http.post(`/api/account/passkeys/${encodeURIComponent(passkey.id)}/delete`, {}).subscribe({
      next: () => {
        this.busy.set(false);
        this.added.set(null);
        this.load();
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.error.set(errorMessage(err));
      },
    });
  }
}
