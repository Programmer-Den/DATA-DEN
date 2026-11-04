import { HttpClient } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { Observable } from 'rxjs';
import { AuthService, errorMessage } from '../auth.service';
import { formatSize } from '../format';

export interface User {
  name: string;
  admin: boolean;
  /** The account from the server's settings; it cannot be changed here. */
  owner: boolean;
  /** Bytes, 0 for no limit. */
  quota: number;
  /** Bytes stored, -1 when unknown. */
  used: number;
  /** Whether the login needs a code from an app. */
  twoFactor: boolean;
}

const GB = 1024 ** 3;

/** "50" for 50 GB, "" for no limit: the value of a quota field. */
export function quotaField(bytes: number): string {
  return bytes > 0 ? String(Math.round((bytes / GB) * 100) / 100) : '';
}

/** Bytes for a quota field in GB; empty or 0 means no limit, null is invalid. */
export function parseQuota(value: string): number | null {
  const text = value.trim().replace(',', '.');
  if (text === '') {
    return 0;
  }
  const gb = Number(text);
  return Number.isFinite(gb) && gb >= 0 ? Math.round(gb * GB) : null;
}

/** The accounts, for admins: create, change, delete. */
@Component({
  selector: 'app-users',
  templateUrl: './users.html',
  styleUrl: './users.css',
})
export class UsersView {
  private readonly http = inject(HttpClient);
  protected readonly auth = inject(AuthService);

  protected readonly users = signal<User[] | null>(null);
  protected readonly error = signal<string | null>(null);
  protected readonly done = signal<string | null>(null);
  protected readonly busy = signal(false);
  protected readonly formatSize = formatSize;
  protected readonly quotaField = quotaField;

  constructor() {
    this.load();
  }

  private load(): void {
    this.http.get<{ users: User[] }>('/api/users').subscribe({
      next: (answer) => this.users.set(answer.users),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected create(event: Event, form: HTMLFormElement): void {
    event.preventDefault();
    const data = new FormData(form);
    const quota = parseQuota(String(data.get('quota') ?? ''));
    if (quota === null) {
      this.fail('Ungültiges Kontingent');
      return;
    }
    const name = String(data.get('name') ?? '').trim();
    const body = {
      name,
      password: String(data.get('password') ?? ''),
      admin: data.get('admin') === 'on',
      quota,
    };
    this.act(this.http.post('/api/users', body), `Konto „${name}“ angelegt`, () => form.reset());
  }

  protected setAdmin(user: User, admin: boolean): void {
    const what = admin ? 'ist jetzt Admin' : 'ist kein Admin mehr';
    this.act(this.http.post(`/api/users/${user.name}`, { admin }), `„${user.name}“ ${what}`);
  }

  protected setQuota(user: User, value: string): void {
    const quota = parseQuota(value);
    if (quota === null) {
      this.fail('Ungültiges Kontingent');
      return;
    }
    const what = quota > 0 ? formatSize(quota) : 'ohne Grenze';
    this.act(
      this.http.post(`/api/users/${user.name}`, { quota }),
      `Kontingent von „${user.name}“: ${what}`,
    );
  }

  protected setPassword(user: User): void {
    const password = prompt(`Neues Passwort für „${user.name}“ (mindestens 10 Zeichen):`);
    if (password) {
      this.act(
        this.http.post(`/api/users/${user.name}`, { password }),
        `Passwort von „${user.name}“ geändert`,
      );
    }
  }

  protected resetTwoFactor(user: User): void {
    if (
      confirm(
        `Die Zwei-Faktor-Anmeldung von „${user.name}“ ausschalten, etwa weil das Handy verloren ist?`,
      )
    ) {
      this.act(
        this.http.post(`/api/users/${user.name}`, { twoFactor: false }),
        `Zwei-Faktor-Anmeldung von „${user.name}“ ausgeschaltet`,
      );
    }
  }

  protected remove(user: User): void {
    if (
      confirm(
        `Das Konto „${user.name}“ löschen? Seine Dateien werden auf dem Server beiseitegelegt, nicht gelöscht.`,
      )
    ) {
      this.act(
        this.http.post(`/api/users/${user.name}/delete`, {}),
        `Konto „${user.name}“ gelöscht`,
      );
    }
  }

  private act(request: Observable<unknown>, message: string, then?: () => void): void {
    this.busy.set(true);
    this.error.set(null);
    this.done.set(null);
    request.subscribe({
      next: () => {
        this.busy.set(false);
        this.done.set(message);
        then?.();
        this.load();
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.error.set(errorMessage(err));
        this.load();
      },
    });
  }

  private fail(message: string): void {
    this.done.set(null);
    this.error.set(message);
  }
}
