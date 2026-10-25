import { HttpClient } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { AuthService, errorMessage } from './auth.service';
import { Files } from './files/files';

interface Health {
  status: string;
  version: string;
}

@Component({
  selector: 'app-root',
  imports: [Files],
  templateUrl: './app.html',
  styleUrl: './app.css',
})
export class App {
  protected readonly auth = inject(AuthService);
  /** The server's version, or null until it answered. */
  protected readonly version = signal<string | null>(null);
  protected readonly offline = signal(false);
  /** Why the last login failed, or null. */
  protected readonly loginError = signal<string | null>(null);
  protected readonly busy = signal(false);

  constructor() {
    inject(HttpClient)
      .get<Health>('/api/health')
      .subscribe({
        next: (health) => this.version.set(health.version),
        error: () => this.offline.set(true),
      });
    this.auth.check();
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
