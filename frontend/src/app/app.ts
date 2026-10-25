import { HttpClient } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';

interface Health {
  status: string;
  version: string;
}

@Component({
  selector: 'app-root',
  templateUrl: './app.html',
  styleUrl: './app.css',
})
export class App {
  /** The server's version, or null until it answered. */
  protected readonly version = signal<string | null>(null);
  protected readonly offline = signal(false);

  constructor() {
    inject(HttpClient)
      .get<Health>('/api/health')
      .subscribe({
        next: (health) => this.version.set(health.version),
        error: () => this.offline.set(true),
      });
  }
}
