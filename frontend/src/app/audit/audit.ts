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
  create: 'Textdatei angelegt',
  edit: 'Bearbeitet',
  office: 'Im Office-Editor bearbeitet',
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
  'grant-create': 'Ordner mit einem Konto geteilt',
  'grant-delete': 'Geteilten Ordner beendet',
  'team-create': 'Team-Ordner angelegt',
  'team-delete': 'Team-Ordner gelöscht',
  'team-member': 'Mitglied eines Team-Ordners gesetzt',
  'team-member-delete': 'Mitglied aus einem Team-Ordner entfernt',
  'share-password-failed': 'Falsches Link-Passwort',
  'integrity-accept': 'Beschädigte Datei als richtig übernommen',
  'version-restore': 'Ältere Version wiederhergestellt',
  'version-delete': 'Ältere Version gelöscht',
};

/** The kinds of entries to pick from, each a list of actions. */
export const KINDS: { name: string; actions: string }[] = [
  { name: 'Alles', actions: '' },
  {
    name: 'Dateien geändert',
    actions:
      'upload,replace,share-upload,create,edit,office,folder,move,version-restore,integrity-accept',
  },
  {
    name: 'Gelöscht und wiederhergestellt',
    actions: 'delete,restore,purge,trash-empty,version-delete',
  },
  {
    name: 'Geteilt',
    actions:
      'share-create,share-delete,share-download,share-password-failed,grant-create,grant-delete,team-create,team-delete,team-member,team-member-delete',
  },
  { name: 'Anmeldungen', actions: 'login,login-failed,logout,token-failed' },
  {
    name: 'Konten',
    actions:
      'password,2fa-on,2fa-off,2fa-reset,token-create,token-revoke,tokens-revoke,user-create,user-change,user-delete',
  },
];

/** How many entries one page shows, as the server does without a limit. */
const PAGE = 200;

const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium', timeStyle: 'medium' });

/**
 * The audit log: who did what and when. Admins see everyone's entries, the
 * others their own and what other accounts did in folders they share.
 */
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
  /** Whether there may be older entries than those shown. */
  protected readonly more = signal(false);
  protected readonly kinds = KINDS;
  private params: Record<string, string> = {};

  constructor() {
    this.load(false);
  }

  /** Loads the newest entries, or with older the page before those shown. */
  private load(older: boolean): void {
    const params = { ...this.params };
    const shown = this.entries() ?? [];
    if (older && shown.length > 0) {
      params['before'] = shown[shown.length - 1].time;
    }
    this.error.set(null);
    this.http.get<{ entries: AuditEntry[] }>('/api/audit', { params }).subscribe({
      next: (answer) => {
        this.entries.set(older ? [...shown, ...answer.entries] : answer.entries);
        this.more.set(answer.entries.length === PAGE);
      },
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected filter(event: Event): void {
    event.preventDefault();
    const form = new FormData(event.target as HTMLFormElement);
    const params: Record<string, string> = {};
    for (const [field, param] of [
      ['user', 'user'],
      ['kind', 'action'],
      ['target', 'target'],
    ]) {
      const value = String(form.get(field) ?? '').trim();
      if (value) params[param] = value;
    }
    this.params = params;
    this.load(false);
  }

  protected older(): void {
    this.load(true);
  }

  protected action(entry: AuditEntry): string {
    return ACTIONS[entry.action] ?? entry.action;
  }

  protected time(entry: AuditEntry): string {
    return date.format(new Date(entry.time));
  }
}
