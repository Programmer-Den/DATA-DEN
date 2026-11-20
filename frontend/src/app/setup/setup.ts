import { HttpClient } from '@angular/common/http';
import { Component, inject, output, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { formatSize } from '../format';
import { Disk, RaidArray } from '../status/status.service';

/** The server's checks of the installation, from POST /api/setup/check. */
export interface Checks {
  dataDir: string;
  writable: boolean;
  writeError?: string;
  disk: Disk | null;
  diskError?: string;
  raid: RaidArray[] | null;
  /** Whether the way from the Pi to the server is encrypted. */
  https: boolean;
  /** The Pi's address, empty when it is not set. */
  trustedProxy: string;
}

/** One line of the checks: fine, or a warning with what to do. */
export interface Check {
  ok: boolean;
  text: string;
  /** What to do about a warning. */
  hint?: string;
}

const percent = new Intl.NumberFormat('de-DE', { maximumFractionDigits: 1 });

/** The checks as lines; origin is the address the page was opened with. */
export function checkList(checks: Checks, origin: string): Check[] {
  const list: Check[] = [];
  list.push(
    checks.writable
      ? { ok: true, text: `Datenordner ${checks.dataDir} ist beschreibbar` }
      : {
          ok: false,
          text: `In den Datenordner ${checks.dataDir} kann nicht geschrieben werden: ${checks.writeError ?? ''}`,
          hint: 'In der VM: sudo chown -R 65532:65532 /srv/data-den',
        },
  );
  list.push(
    checks.disk
      ? {
          ok: checks.disk.level === 'ok',
          text: `${formatSize(checks.disk.free)} frei von ${formatSize(checks.disk.total)}`,
        }
      : { ok: false, text: checks.diskError || 'Speicherplatz unbekannt' },
  );
  const arrays = checks.raid ?? [];
  if (arrays.length === 0) {
    list.push({
      ok: false,
      text: 'Kein RAID gefunden: die Daten liegen nur auf einer Platte',
      hint: 'In der VM: sudo vm/install.sh (vm/README.md, Schritt 4)',
    });
  }
  for (const array of arrays) {
    let text = `RAID ${array.name} (${array.level || 'unbekannt'}): ${array.working} von ${array.disks} Platten`;
    if (array.sync) {
      text += `, Abgleich ${percent.format(array.progress ?? 0)} % (die Platten sind schon nutzbar)`;
    }
    list.push(
      array.healthy
        ? { ok: true, text }
        : { ok: false, text, hint: 'Siehe vm/README.md, „When a disk fails“' },
    );
  }
  list.push(
    checks.https
      ? { ok: true, text: 'Der Weg vom Pi zur VM ist verschlüsselt (HTTPS)' }
      : {
          ok: false,
          text: 'Der Weg vom Pi zur VM ist nicht verschlüsselt',
          hint: 'DATA_DEN_TLS_CERT und DATA_DEN_TLS_KEY setzen (deploy/README.md, Schritt 4)',
        },
  );
  list.push(
    checks.trustedProxy
      ? { ok: true, text: `Der Pi hat die Adresse ${checks.trustedProxy}` }
      : {
          ok: false,
          text: 'Die Adresse des Pi ist nicht eingetragen, so zählen Fehlversuche aller Besucher zusammen',
          hint: 'DATA_DEN_TRUSTED_PROXY in der .env setzen (deploy/README.md, Schritt 5)',
        },
  );
  const url = new URL(origin);
  const local = url.hostname === 'localhost' || url.hostname === '127.0.0.1';
  list.push(
    url.protocol === 'https:' || local
      ? { ok: true, text: `Erreichbar unter ${url.host}` }
      : {
          ok: false,
          text: `Diese Seite ist ohne HTTPS geöffnet (${url.host})`,
          hint: 'GATEWAY_DOMAIN in der rpi-setup.env des Pi setzen und die Seite über https:// öffnen',
        },
  );
  return list;
}

/**
 * The first run, before there is an owner's account: the setup code from the
 * VM, the checks of the installation, then the account.
 */
@Component({
  selector: 'app-setup',
  templateUrl: './setup.html',
  styleUrl: './setup.css',
})
export class SetupView {
  private readonly http = inject(HttpClient);
  /** Emits the owner's name once the account is created. */
  readonly done = output<string>();

  protected readonly step = signal<'code' | 'checks' | 'account'>('code');
  protected readonly code = signal('');
  protected readonly checks = signal<Check[]>([]);
  /** Whether the server can save the account. */
  protected readonly ready = signal(false);
  protected readonly error = signal<string | null>(null);
  protected readonly busy = signal(false);

  protected check(event: Event | null, code: string): void {
    event?.preventDefault();
    this.busy.set(true);
    this.error.set(null);
    this.http.post<Checks>('/api/setup/check', { code: code.trim() }).subscribe({
      next: (checks) => {
        this.busy.set(false);
        this.code.set(code.trim());
        this.checks.set(checkList(checks, location.origin));
        this.ready.set(checks.writable);
        this.step.set('checks');
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.error.set(errorMessage(err));
      },
    });
  }

  protected recheck(): void {
    this.check(null, this.code());
  }

  protected create(event: Event, username: string, password: string, repeat: string): void {
    event.preventDefault();
    if (password !== repeat) {
      this.error.set('Die beiden Passwörter sind verschieden');
      return;
    }
    this.busy.set(true);
    this.error.set(null);
    this.http
      .post<{ username: string }>('/api/setup', { code: this.code(), username, password })
      .subscribe({
        next: (answer) => {
          this.busy.set(false);
          this.done.emit(answer.username);
        },
        error: (err: unknown) => {
          this.busy.set(false);
          this.error.set(errorMessage(err));
        },
      });
  }
}
