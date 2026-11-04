import { HttpClient } from '@angular/common/http';
import { Component, DestroyRef, effect, inject, signal, untracked } from '@angular/core';
import { AuthService, errorMessage } from './auth.service';
import { Files } from './files/files';
import { Kind } from './media/item';
import { MediaView } from './media/media';
import { StatusView } from './status/status';
import { StatusService } from './status/status.service';
import { TrashView } from './trash/trash';

type View = 'files' | Kind | 'trash' | 'status';

/** How often the warnings about space and the RAID are checked. */
export const STATUS_INTERVAL = 5 * 60 * 1000;

interface Health {
  status: string;
  version: string;
}

@Component({
  selector: 'app-root',
  imports: [Files, MediaView, TrashView, StatusView],
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
  protected readonly tabs: { view: View; name: string }[] = [
    { view: 'files', name: 'Dateien' },
    { view: 'photo', name: 'Fotos' },
    { view: 'video', name: 'Videos' },
    { view: 'document', name: 'Dokumente' },
    { view: 'trash', name: 'Papierkorb' },
    { view: 'status', name: 'Status' },
  ];
  protected readonly view = signal<View>('files');

  constructor() {
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
    this.busy.set(true);
    this.loginError.set(null);
    this.auth.login(username, password).subscribe({
      next: () => this.busy.set(false),
      error: (err: unknown) => {
        this.busy.set(false);
        this.loginError.set(errorMessage(err));
      },
    });
  }

  protected logout(): void {
    this.auth.logout().subscribe();
  }
}
