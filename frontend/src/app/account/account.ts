import { HttpClient } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { AuthService, errorMessage } from '../auth.service';
import { formatSize } from '../format';

export interface Usage {
  used: number;
  /** 0 means no limit. */
  quota: number;
}

/** One's own account: storage used and the password. */
@Component({
  selector: 'app-account',
  templateUrl: './account.html',
  styleUrl: './account.css',
})
export class AccountView {
  private readonly http = inject(HttpClient);
  protected readonly auth = inject(AuthService);

  protected readonly usage = signal<Usage | null>(null);
  protected readonly error = signal<string | null>(null);
  protected readonly done = signal<string | null>(null);
  protected readonly busy = signal(false);
  protected readonly formatSize = formatSize;

  constructor() {
    this.http.get<Usage>('/api/usage').subscribe({
      next: (usage) => this.usage.set(usage),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  /** How full the quota is, in percent (0 to 100). */
  protected percent(usage: Usage): number {
    return usage.quota > 0 ? Math.min(100, Math.round((usage.used / usage.quota) * 100)) : 0;
  }

  protected changePassword(event: Event, form: HTMLFormElement): void {
    event.preventDefault();
    const data = new FormData(form);
    const current = String(data.get('current') ?? '');
    const password = String(data.get('password') ?? '');
    if (password !== String(data.get('repeat') ?? '')) {
      this.done.set(null);
      this.error.set('Die beiden neuen Passwörter sind verschieden');
      return;
    }
    this.busy.set(true);
    this.error.set(null);
    this.done.set(null);
    this.http.post('/api/account/password', { current, password }).subscribe({
      next: () => {
        this.busy.set(false);
        form.reset();
        this.done.set('Passwort geändert. Andere Geräte müssen sich neu anmelden.');
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.error.set(errorMessage(err));
      },
    });
  }
}
