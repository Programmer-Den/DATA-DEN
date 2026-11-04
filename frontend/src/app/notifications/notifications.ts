import { HttpClient } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { PushDevice } from './push-device';

/** How an event reaches the user. */
export interface Ways {
  email: boolean;
  push: boolean;
}

/** An event the user can choose. */
export interface KindInfo {
  kind: string;
  name: string;
  /** About the NAS as a whole; only for admins. */
  system: boolean;
}

/** The answer of GET /api/notifications. */
export interface NotificationSettings {
  email: string;
  events: Record<string, Ways>;
  kinds: KindInfo[];
  /** Whether the server can send email. */
  mail: boolean;
  /** The server's key for web push, empty when it cannot send any. */
  pushKey: string;
  /** The endpoints of the browsers that get push messages. */
  devices: string[];
}

/** What a test message reached. */
export interface TestResult {
  /** "ok", why it failed, or missing when no email was sent. */
  email?: string;
  devices: number;
  failed: number;
}

/** Which events reach the user by email and by push, and on which devices. */
@Component({
  selector: 'app-notifications',
  templateUrl: './notifications.html',
  styleUrl: './notifications.css',
})
export class NotificationsView {
  private readonly http = inject(HttpClient);
  private readonly device = inject(PushDevice);
  /** Whether this browser can get push messages at all. */
  protected readonly supported = this.device.supported();
  /** Whether the user blocked notifications for DATA-DEN. */
  protected readonly blocked = signal(this.device.blocked());

  protected readonly settings = signal<NotificationSettings | null>(null);
  protected readonly email = signal('');
  protected readonly events = signal<Record<string, Ways>>({});
  /** Whether this browser is among the devices. */
  protected readonly here = signal(false);
  protected readonly error = signal<string | null>(null);
  protected readonly done = signal<string | null>(null);
  protected readonly busy = signal(false);

  constructor() {
    this.load();
  }

  private load(): void {
    this.http.get<NotificationSettings>('/api/notifications').subscribe({
      next: (settings) => {
        this.settings.set(settings);
        this.email.set(settings.email);
        this.events.set(settings.events);
        void this.device
          .endpoint()
          .then((endpoint) => this.here.set(!!endpoint && settings.devices.includes(endpoint)));
      },
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected ways(kind: string): Ways {
    return this.events()[kind] ?? { email: false, push: false };
  }

  protected toggle(kind: string, way: keyof Ways, on: boolean): void {
    this.events.update((events) => ({ ...events, [kind]: { ...this.ways(kind), [way]: on } }));
  }

  protected save(event: Event): void {
    event.preventDefault();
    this.start();
    this.http
      .post('/api/notifications', { email: this.email().trim(), events: this.events() })
      .subscribe({
        next: () => this.finish('Gespeichert'),
        error: (err: unknown) => this.failed(err),
      });
  }

  protected async turnOn(): Promise<void> {
    const key = this.settings()?.pushKey;
    if (!key) {
      return;
    }
    this.start();
    let subscription: PushSubscriptionJSON | null;
    try {
      subscription = await this.device.subscribe(key);
    } catch {
      this.busy.set(false);
      this.error.set('Dieser Browser konnte keine Push-Nachrichten einrichten');
      return;
    }
    if (!subscription) {
      this.blocked.set(this.device.blocked());
      this.busy.set(false);
      this.error.set('Benachrichtigungen sind für DATA-DEN im Browser blockiert');
      return;
    }
    this.http.post('/api/notifications/subscribe', subscription).subscribe({
      next: () => {
        this.here.set(true);
        this.finish('Push-Nachrichten auf diesem Gerät eingeschaltet');
        this.load();
      },
      error: (err: unknown) => this.failed(err),
    });
  }

  protected async turnOff(): Promise<void> {
    this.start();
    let endpoint: string | null = null;
    try {
      endpoint = await this.device.unsubscribe();
    } catch {
      // The server forgets it anyway.
    }
    if (!endpoint) {
      this.here.set(false);
      this.finish('Push-Nachrichten auf diesem Gerät ausgeschaltet');
      return;
    }
    this.http.post('/api/notifications/unsubscribe', { endpoint }).subscribe({
      next: () => {
        this.here.set(false);
        this.finish('Push-Nachrichten auf diesem Gerät ausgeschaltet');
        this.load();
      },
      error: (err: unknown) => this.failed(err),
    });
  }

  protected test(): void {
    this.start();
    this.http.post<TestResult>('/api/notifications/test', {}).subscribe({
      next: (result) => this.finish(describe(result)),
      error: (err: unknown) => this.failed(err),
    });
  }

  private start(): void {
    this.busy.set(true);
    this.error.set(null);
    this.done.set(null);
  }

  private finish(done: string): void {
    this.busy.set(false);
    this.done.set(done);
  }

  private failed(err: unknown): void {
    this.busy.set(false);
    this.error.set(errorMessage(err));
  }
}

/** Says what a test message reached. */
export function describe(result: TestResult): string {
  const parts: string[] = [];
  if (result.email === 'ok') {
    parts.push('E-Mail gesendet');
  } else if (result.email) {
    parts.push(`E-Mail: ${result.email}`);
  }
  if (result.devices > 0) {
    parts.push(`Push an ${result.devices} ${result.devices === 1 ? 'Gerät' : 'Geräte'} gesendet`);
  }
  if (result.failed > 0) {
    parts.push(`${result.failed} ${result.failed === 1 ? 'Gerät' : 'Geräte'} nicht erreicht`);
  }
  return parts.length > 0
    ? parts.join(', ')
    : 'Nichts gesendet: trage eine E-Mail-Adresse ein oder schalte Push ein';
}
