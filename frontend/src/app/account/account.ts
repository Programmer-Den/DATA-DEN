import { HttpClient } from '@angular/common/http';
import { Component, computed, inject, signal } from '@angular/core';
import { DomSanitizer, SafeUrl } from '@angular/platform-browser';
import { AuthService, errorMessage } from '../auth.service';
import { formatSize } from '../format';
import { TokensView } from './tokens';

export interface Usage {
  used: number;
  /** 0 means no limit. */
  quota: number;
}

export interface TwoFactorState {
  enabled: boolean;
  /** Unused recovery codes. */
  recoveryLeft: number;
}

export interface TwoFactorSetup {
  /** The key to type into the app, base32. */
  secret: string;
  /** otpauth:// link, which opens the app on a phone. */
  uri: string;
  /** The QR code of uri, as a data: URL. */
  qr: string;
}

/** One's own account: storage used, password, two-factor login and app passwords. */
@Component({
  selector: 'app-account',
  imports: [TokensView],
  templateUrl: './account.html',
  styleUrl: './account.css',
})
export class AccountView {
  private readonly http = inject(HttpClient);
  protected readonly auth = inject(AuthService);
  private readonly sanitizer = inject(DomSanitizer);

  protected readonly usage = signal<Usage | null>(null);
  protected readonly error = signal<string | null>(null);
  protected readonly done = signal<string | null>(null);
  protected readonly busy = signal(false);
  protected readonly formatSize = formatSize;
  protected readonly twoFactor = signal<TwoFactorState | null>(null);
  /** A secret being set up, until the first code fits. */
  protected readonly setup = signal<TwoFactorSetup | null>(null);
  /** The otpauth:// link of the setup, which Angular would otherwise block. */
  protected readonly appLink = computed<SafeUrl | null>(() => {
    const uri = this.setup()?.uri;
    return uri?.startsWith('otpauth://totp/') ? this.sanitizer.bypassSecurityTrustUrl(uri) : null;
  });
  /** The recovery codes, shown once right after turning it on. */
  protected readonly recovery = signal<string[] | null>(null);

  constructor() {
    this.http.get<Usage>('/api/usage').subscribe({
      next: (usage) => this.usage.set(usage),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
    this.loadTwoFactor();
  }

  private loadTwoFactor(): void {
    this.http.get<TwoFactorState>('/api/account/2fa').subscribe({
      next: (state) => {
        this.twoFactor.set(state);
        this.auth.twoFactor.set(state.enabled);
      },
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected startSetup(): void {
    this.start();
    this.http.post<TwoFactorSetup>('/api/account/2fa/setup', {}).subscribe({
      next: (setup) => {
        this.busy.set(false);
        this.setup.set(setup);
      },
      error: (err: unknown) => this.failed(err),
    });
  }

  protected enable(event: Event, code: string): void {
    event.preventDefault();
    this.start();
    this.http
      .post<{ recovery: string[] }>('/api/account/2fa/enable', { code: code.trim() })
      .subscribe({
        next: (answer) => {
          this.busy.set(false);
          this.setup.set(null);
          this.recovery.set(answer.recovery);
          this.done.set('Zwei-Faktor-Anmeldung ist an');
          this.loadTwoFactor();
        },
        error: (err: unknown) => this.failed(err),
      });
  }

  protected disable(event: Event, form: HTMLFormElement): void {
    event.preventDefault();
    const data = new FormData(form);
    const body = {
      password: String(data.get('password') ?? ''),
      code: String(data.get('code') ?? '').trim(),
    };
    this.start();
    this.http.post('/api/account/2fa/disable', body).subscribe({
      next: () => {
        this.busy.set(false);
        this.recovery.set(null);
        this.done.set('Zwei-Faktor-Anmeldung ist aus');
        this.loadTwoFactor();
      },
      error: (err: unknown) => this.failed(err),
    });
  }

  private start(): void {
    this.busy.set(true);
    this.error.set(null);
    this.done.set(null);
  }

  private failed(err: unknown): void {
    this.busy.set(false);
    this.error.set(errorMessage(err));
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
