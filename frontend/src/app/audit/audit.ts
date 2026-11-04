import { HttpClient } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { AuthService, errorMessage } from '../auth.service';

export interface AuditEntry {
  time: string;
  user: string;
  ip?: string;
  action: string;
  target?: string;
}

/** What each action of the audit log means, in German like the UI. */
export const ACTIONS: Record<string, string> = {
  login: 'Angemeldet',
  'login-failed': 'Anmeldung fehlgeschlagen',
  logout: 'Abgemeldet',
  password: 'Passwort geändert',
  '2fa-on': 'Zwei-Faktor-Anmeldung eingeschaltet',
  '2fa-off': 'Zwei-Faktor-Anmeldung ausgeschaltet',
  '2fa-reset': 'Zwei-Faktor-Anmeldung eines Kontos ausgeschaltet',
  'token-create': 'App-Passwort angelegt',
  'token-revoke': 'App-Passwort widerrufen',
  'token-failed': 'Falsches App-Passwort',
  'tokens-revoke': 'App-Passwörter eines Kontos widerrufen',
  'user-create': 'Konto angelegt',
  'user-change': 'Konto geändert',
  'user-delete': 'Konto gelöscht',
  upload: 'Hochgeladen',
  replace: 'Ersetzt (alte Version aufbewahrt)',
  folder: 'Ordner angelegt',
  move: 'Verschoben oder umbenannt',
  delete: 'In den Papierkorb',
  restore: 'Wiederhergestellt',
  purge: 'Endgültig gelöscht',
  'trash-empty': 'Papierkorb geleert',
  'share-create': 'Link erstellt',
  'share-delete': 'Link gelöscht',
  'share-download': 'Über einen Link geladen',
  'share-upload': 'Über einen Upload-Link hochgeladen',
  'share-password-failed': 'Falsches Link-Passwort',
  'integrity-accept': 'Beschädigte Datei als richtig übernommen',
  'version-restore': 'Ältere Version wiederhergestellt',
  'version-delete': 'Ältere Version gelöscht',
};

const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium', timeStyle: 'medium' });

/** The audit log: who did what and when. Admins see everyone's entries. */
@Component({
  selector: 'app-audit',
  templateUrl: './audit.html',
  styleUrl: './audit.css',
})
export class AuditView {
  private readonly http = inject(HttpClient);
  protected readonly auth = inject(AuthService);

  protected readonly entries = signal<AuditEntry[] | null>(null);
  protected readonly error = signal<string | null>(null);

  constructor() {
    this.load('');
  }

  protected load(user: string): void {
    const params: Record<string, string> = user.trim() ? { user: user.trim() } : {};
    this.error.set(null);
    this.http.get<{ entries: AuditEntry[] }>('/api/audit', { params }).subscribe({
      next: (answer) => this.entries.set(answer.entries),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected filter(event: Event, user: string): void {
    event.preventDefault();
    this.load(user);
  }

  protected action(entry: AuditEntry): string {
    return ACTIONS[entry.action] ?? entry.action;
  }

  protected time(entry: AuditEntry): string {
    return date.format(new Date(entry.time));
  }
}
