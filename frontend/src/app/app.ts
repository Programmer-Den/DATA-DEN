import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, DestroyRef, computed, effect, inject, signal, untracked } from '@angular/core';
import { AccountView } from './account/account';
import { AuditView } from './audit/audit';
import { AuthService, errorMessage, needsCode } from './auth.service';
import { Files } from './files/files';
import { MarksView } from './marks/marks';
import { Kind } from './media/item';
import { MediaView } from './media/media';
import { NotificationsView } from './notifications/notifications';
import { ChecksView } from './checks/checks';
import { OfficeFormats } from './office/formats';
import { passkeyError, passkeysSupported } from './passkey';
import { StartFolder } from './files/start-folder';
import { PublicShare } from './public/public-share';
import { SearchView } from './search/search';
import { SetupView } from './setup/setup';
import { SharesView } from './shares/shares';
import { Shared } from './shared/shared';
import { StatusView } from './status/status';
import { StatusService } from './status/status.service';
import { TrashView } from './trash/trash';
import { TeamsView } from './teams/teams';
import { UsersView } from './users/users';
import { VaultView } from './vault/vault';

type View =
  | 'files'
  | 'marks'
  | Kind
  | 'vault'
  | 'shares'
  | 'shared'
  | 'trash'
  | 'status'
  | 'checks'
  | 'account'
  | 'notifications'
  | 'users'
  | 'teams'
  | 'search'
  | 'audit';

/** How often the warnings about space, the quota and the RAID are checked. */
export const STATUS_INTERVAL = 5 * 60 * 1000;

interface Health {
  status: string;
  version: string;
  /** True before there is an owner's account. */
  setup?: boolean;
}

/** How often the page asks whether the server is back after the setup. */
export const SETUP_POLL = 1000;

@Component({
  selector: 'app-root',
  imports: [
    Files,
    MarksView,
    MediaView,
    PublicShare,
    SearchView,
    SetupView,
    SharesView,
    Shared,
    TrashView,
    StatusView,
    ChecksView,
    AccountView,
    UsersView,
    TeamsView,
    AuditView,
    NotificationsView,
    VaultView,
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
  /** Whether the server waits for its setup. */
  protected readonly setup = signal(false);
  /** The owner's name right after the setup, for the login form. */
  protected readonly newOwner = signal('');
  /** Why the last login failed, or null. */
  protected readonly loginError = signal<string | null>(null);
  protected readonly busy = signal(false);
  /** Whether the login form asks for the code of the second factor. */
  protected readonly askCode = signal(false);
  /** Whether the login form offers a passkey. */
  protected readonly passkeys = signal(false);
  protected readonly tabs = computed<{ view: View; name: string }[]>(() => [
    { view: 'files', name: 'Dateien' },
    { view: 'marks', name: 'Favoriten' },
    { view: 'photo', name: 'Fotos' },
    { view: 'video', name: 'Videos' },
    { view: 'document', name: 'Dokumente' },
    { view: 'vault', name: 'Tresor' },
    { view: 'shares', name: 'Freigaben' },
    { view: 'shared', name: 'Geteilt mit mir' },
    { view: 'trash', name: 'Papierkorb' },
    { view: 'status', name: 'Status' },
    { view: 'checks', name: 'Prüfung' },
    { view: 'account', name: 'Konto' },
    { view: 'notifications', name: 'Benachrichtigungen' },
    ...(this.auth.admin()
      ? [
          { view: 'users' as const, name: 'Benutzer' },
          { view: 'teams' as const, name: 'Team-Ordner' },
        ]
      : []),
    { view: 'audit', name: 'Protokoll' },
  ]);
  protected readonly view = signal<View>('files');
  /** The id of a share link /s/<id> this page was opened with, or null. */
  protected readonly shareId = shareIdOf(location.pathname);
  /** What was searched for last. */
  protected readonly query = signal('');
  /** The tag the favourites view shows, picked in the folder view. */
  protected readonly tag = signal('');
  private readonly startFolder = inject(StartFolder);
  private readonly office = inject(OfficeFormats);
  private readonly http = inject(HttpClient);

  constructor() {
    if (this.shareId) {
      // Someone with a link: no login, no version.
      return;
    }
    this.http.get<Health>('/api/health').subscribe({
      next: (health) => {
        this.version.set(health.version);
        this.setup.set(health.setup === true);
      },
      error: () => this.offline.set(true),
    });
    this.auth.check();

    // Whether the login form offers a passkey, asked once it is shown.
    let askedPasskeys = false;
    effect(() => {
      if (this.auth.user() === null && !askedPasskeys && passkeysSupported()) {
        askedPasskeys = true;
        this.auth.passkeysOffered().subscribe({
          next: (offered) => this.passkeys.set(offered),
          error: () => this.passkeys.set(false),
        });
      }
    });

    // Warn about low space or a broken mirror on every page, not only on the
    // status page.
    effect(() => {
      if (this.auth.user()) {
        untracked(() => {
          this.status.refresh();
          this.status.refreshUsage();
          this.office.load();
        });
      }
    });
    const timer = setInterval(() => {
      if (this.auth.user()) {
        this.status.refresh();
        this.status.refreshUsage();
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

  protected loginWithPasskey(): void {
    this.busy.set(true);
    this.loginError.set(null);
    this.auth.loginWithPasskey().subscribe({
      next: () => {
        this.busy.set(false);
        this.askCode.set(false);
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.loginError.set(
          err instanceof HttpErrorResponse ? errorMessage(err) : passkeyError(err),
        );
      },
    });
  }

  /** The owner's account is created: waits for the full server, then shows the login. */
  protected setupDone(username: string): void {
    this.newOwner.set(username);
    const poll = () =>
      this.http.get<Health>('/api/health').subscribe({
        next: (health) => {
          if (health.setup) {
            setTimeout(poll, SETUP_POLL);
            return;
          }
          this.setup.set(false);
          this.auth.check();
        },
        // Between the setup's server and the full one.
        error: () => setTimeout(poll, SETUP_POLL),
      });
    poll();
  }

  protected search(event: Event, query: string): void {
    event.preventDefault();
    if (query.trim()) {
      this.query.set(query.trim());
      this.view.set('search');
    }
  }

  /** Shows the files of a tag in the favourites view. */
  protected showTag(tag: string): void {
    this.tag.set(tag);
    this.view.set('marks');
  }

  /** Shows a folder, such as one found in the search, in the folder view. */
  protected showFolder(path: string): void {
    this.startFolder.set(path);
    this.view.set('files');
  }

  protected logout(): void {
    this.auth.logout().subscribe(() => this.view.set('files'));
  }
}

/** The id in a share link's path "/s/<id>", or null for any other page. */
export function shareIdOf(path: string): string | null {
  return /^\/s\/([0-9a-f]{32})\/?$/.exec(path)?.[1] ?? null;
}
