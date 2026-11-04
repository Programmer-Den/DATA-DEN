import { HttpClient } from '@angular/common/http';
import { Component, DestroyRef, computed, effect, inject, signal, untracked } from '@angular/core';
import { AccountView } from './account/account';
import { AuthService, errorMessage, needsCode } from './auth.service';
import { Files } from './files/files';
import { Kind } from './media/item';
import { MediaView } from './media/media';
import { PublicShare } from './public/public-share';
import { SharesView } from './shares/shares';
import { StatusView } from './status/status';
import { StatusService } from './status/status.service';
import { TrashView } from './trash/trash';
import { UsersView } from './users/users';

type View = 'files' | Kind | 'shares' | 'trash' | 'status' | 'account' | 'users';

/** How often the warnings about space and the RAID are checked. */
export const STATUS_INTERVAL = 5 * 60 * 1000;

interface Health {
  status: string;
  version: string;
}

@Component({
  selector: 'app-root',
  imports: [
    Files,
    MediaView,
    PublicShare,
    SharesView,
    TrashView,
    StatusView,
    AccountView,
    UsersView,
  ],
  templateUrl: './app.html',
  styleUrl: './app.css',
})
export class App {
  protected readonly auth = inject(AuthService);
  protected readonly status = inject(StatusService);
  /** The server's version, or null until it answered. */
  protected readonly version = signal<string | null>(null);
  protected readonly offline = signal(false);
  /** Why the last login failed, or null. */
  protected readonly loginError = signal<string | null>(null);
  protected readonly busy = signal(false);
  /** Whether the login form asks for the code of the second factor. */
  protected readonly askCode = signal(false);
  protected readonly tabs = computed<{ view: View; name: string }[]>(() => [
    { view: 'files', name: 'Dateien' },
    { view: 'photo', name: 'Fotos' },
    { view: 'video', name: 'Videos' },
    { view: 'document', name: 'Dokumente' },
    { view: 'shares', name: 'Freigaben' },
    { view: 'trash', name: 'Papierkorb' },
    { view: 'status', name: 'Status' },
    { view: 'account', name: 'Konto' },
    ...(this.auth.admin() ? [{ view: 'users' as const, name: 'Benutzer' }] : []),
  ]);
  protected readonly view = signal<View>('files');
  /** The id of a share link /s/<id> this page was opened with, or null. */
  protected readonly shareId = shareIdOf(location.pathname);

  constructor() {
    if (this.shareId) {
      // Someone with a link: no login, no version.
      return;
    }
    inject(HttpClient)
      .get<Health>('/api/health')
      .subscribe({
        next: (health) => this.version.set(health.version),
        error: () => this.offline.set(true),
      });
    this.auth.check();

    // Warn about low space or a broken mirror on every page, not only on the
    // status page.
    effect(() => {
      if (this.auth.user()) {
        untracked(() => this.status.refresh());
      }
    });
    const timer = setInterval(() => {
      if (this.auth.user()) {
        this.status.refresh();
      }
    }, STATUS_INTERVAL);
    inject(DestroyRef).onDestroy(() => clearInterval(timer));
  }

  protected login(event: Event, username: string, password: string): void {
    event.preventDefault();
    const form = event.target as HTMLFormElement;
    const field = form.elements.namedItem('code') as HTMLInputElement | null;
    const code = field?.value ?? '';
    this.busy.set(true);
    this.loginError.set(null);
    this.auth.login(username, password, code.trim()).subscribe({
      next: () => {
        this.busy.set(false);
        this.askCode.set(false);
      },
      error: (err: unknown) => {
        this.busy.set(false);
        if (needsCode(err)) {
          this.askCode.set(true);
        }
        this.loginError.set(errorMessage(err));
      },
    });
  }

  protected logout(): void {
    this.auth.logout().subscribe(() => this.view.set('files'));
  }
}

/** The id in a share link's path "/s/<id>", or null for any other page. */
export function shareIdOf(path: string): string | null {
  return /^\/s\/([0-9a-f]{32})\/?$/.exec(path)?.[1] ?? null;
}
