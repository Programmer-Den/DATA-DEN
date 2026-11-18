import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, computed, inject, signal } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { errorMessage } from '../auth.service';
import { formatSize } from '../format';
import {
  NewKeys,
  Secret,
  Vault,
  VaultKeys,
  changePassphrase,
  createVault,
  newRecovery,
  openFile,
  openName,
  plainSize,
  sealFile,
  sealName,
  unlock,
} from './vault-crypto';
import { VaultService } from './vault.service';

/** The shortest passphrase: it is all that guards the vault. */
export const MIN_PASSPHRASE = 12;
/** The largest file, as on the server: the browser holds it in memory. */
export const MAX_FILE = 1 << 30;

/** One file or folder as the server lists it, under its sealed name. */
interface SealedEntry {
  name: string;
  dir: boolean;
  size: number;
  modified: string;
}

/** One entry with its name opened. */
export interface VaultEntry extends SealedEntry {
  sealed: string;
  name: string;
}

/** A folder on the way to the one shown. */
interface Crumb {
  sealed: string;
  name: string;
}

const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium', timeStyle: 'short' });

/**
 * The vault (Tresor): files encrypted in the browser before they leave it,
 * so the server, an admin or a thief of the disks cannot read them.
 */
@Component({
  selector: 'app-vault',
  templateUrl: './vault.html',
  styleUrl: './vault.css',
})
export class VaultView {
  private readonly http = inject(HttpClient);
  protected readonly service = inject(VaultService);
  protected readonly formatSize = formatSize;
  protected readonly plainSize = plainSize;
  protected readonly minPassphrase = MIN_PASSPHRASE;

  /** The wrapped keys; null before the vault is set up, undefined until loaded. */
  protected readonly keys = signal<VaultKeys | null | undefined>(undefined);
  protected readonly error = signal<string | null>(null);
  protected readonly done = signal<string | null>(null);
  protected readonly busy = signal(false);
  /** A new vault or recovery key waiting for the user to note the key. */
  protected readonly pending = signal<(NewKeys & { recovery: string; vault?: Vault }) | null>(null);
  /** Whether the unlock form takes the recovery key instead of the passphrase. */
  protected readonly byRecovery = signal(false);
  /** Opened with the recovery key: suggest a new passphrase. */
  protected readonly recovered = signal(false);
  protected readonly path = signal<Crumb[]>([]);
  protected readonly entries = signal<VaultEntry[] | null>(null);
  protected readonly unreadable = signal(0);
  protected readonly settings = signal(false);
  protected readonly unlocked = computed(() => this.service.vault() !== null);

  constructor() {
    this.http.get<{ keys: VaultKeys | null }>('/api/vault').subscribe({
      next: (state) => {
        this.keys.set(state.keys);
        if (this.service.vault()) {
          void this.load();
        }
      },
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected modified(entry: VaultEntry): string {
    return date.format(new Date(entry.modified));
  }

  /** Sets up a new vault: the recovery key is shown before it is stored. */
  protected async create(event: Event, passphrase: string, repeat: string): Promise<void> {
    event.preventDefault();
    if (!this.checkPassphrase(passphrase, repeat)) {
      return;
    }
    await this.run(async () => {
      this.pending.set(await createVault(passphrase));
    });
  }

  /** Stores the new keys once the recovery key was noted. */
  protected async confirm(): Promise<void> {
    const pending = this.pending();
    if (!pending) {
      return;
    }
    await this.run(async () => {
      const stored = await firstValueFrom(
        this.http.post<{ keys: VaultKeys }>('/api/vault/keys', {
          keys: pending.keys,
          proof: pending.proof,
        }),
      );
      this.keys.set(stored.keys);
      this.pending.set(null);
      if (pending.vault) {
        this.service.open(pending.vault);
        this.done.set('Tresor eingerichtet');
        await this.load();
      } else {
        this.done.set('Neuer Wiederherstellungsschlüssel gespeichert, der alte gilt nicht mehr');
      }
    });
  }

  protected async unlock(event: Event, secret: string): Promise<void> {
    event.preventDefault();
    const keys = this.keys();
    if (!keys) {
      return;
    }
    const recovery = this.byRecovery();
    await this.run(async () => {
      this.service.open(
        await unlock(keys, recovery ? { recovery: secret } : { passphrase: secret }),
      );
      this.recovered.set(recovery);
      this.settings.set(recovery);
      this.path.set([]);
      await this.load();
    });
  }

  protected lock(): void {
    this.service.lock();
    this.entries.set(null);
    this.path.set([]);
    this.settings.set(false);
    this.done.set('Tresor gesperrt');
  }

  /** The sealed path of the folder shown, or of a name in it. */
  private sealedPath(name?: string): string {
    const parts = this.path().map((c) => c.sealed);
    if (name) {
      parts.push(name);
    }
    return '/' + parts.join('/');
  }

  private async load(): Promise<void> {
    const vault = this.service.vault();
    if (!vault) {
      return;
    }
    const listing = await firstValueFrom(
      this.http.get<{ entries: SealedEntry[] }>('/api/vault/files', {
        params: { path: this.sealedPath() },
      }),
    );
    const entries: VaultEntry[] = [];
    let unreadable = 0;
    for (const entry of listing.entries) {
      const name = await openName(vault, entry.name);
      if (name === null) {
        unreadable++;
        continue;
      }
      entries.push({ ...entry, sealed: entry.name, name });
    }
    entries.sort((a, b) =>
      a.dir !== b.dir ? (a.dir ? -1 : 1) : a.name.localeCompare(b.name, 'de'),
    );
    this.entries.set(entries);
    this.unreadable.set(unreadable);
  }

  protected async openFolder(entry: VaultEntry): Promise<void> {
    await this.run(async () => {
      this.path.update((p) => [...p, { sealed: entry.sealed, name: entry.name }]);
      await this.load();
    });
  }

  /** Goes up to the folder at depth (0 for the vault itself). */
  protected async up(depth: number): Promise<void> {
    await this.run(async () => {
      this.path.update((p) => p.slice(0, depth));
      await this.load();
    });
  }

  /** Whether the name is taken in the folder shown: the server cannot tell. */
  private taken(name: string): boolean {
    return (this.entries() ?? []).some((e) => e.name === name);
  }

  protected async createFolder(event: Event, input: HTMLInputElement): Promise<void> {
    event.preventDefault();
    const name = input.value.trim();
    const vault = this.service.vault();
    if (!vault || !validName(name, this.error)) {
      return;
    }
    if (this.taken(name)) {
      this.error.set(`„${name}“ gibt es hier schon`);
      return;
    }
    await this.run(async () => {
      await firstValueFrom(
        this.http.post('/api/vault/folders', {
          path: this.sealedPath(await sealName(vault, name)),
        }),
      );
      input.value = '';
      this.done.set(`Ordner „${name}“ angelegt`);
      await this.load();
    });
  }

  /** Encrypts each file in the browser and uploads only the ciphertext. */
  protected async upload(input: HTMLInputElement): Promise<void> {
    const vault = this.service.vault();
    const files = [...(input.files ?? [])];
    input.value = '';
    if (!vault || files.length === 0) {
      return;
    }
    await this.run(async () => {
      let count = 0;
      for (const file of files) {
        if (!validName(file.name, this.error)) {
          return;
        }
        if (this.taken(file.name)) {
          this.error.set(`„${file.name}“ gibt es hier schon`);
          return;
        }
        if (file.size > MAX_FILE) {
          this.error.set(`„${file.name}“ ist größer als 1 GB`);
          return;
        }
        this.done.set(`Verschlüssele „${file.name}“ …`);
        const sealed = await sealFile(vault, file);
        const name = await sealName(vault, file.name);
        await firstValueFrom(
          this.http.put('/api/vault/file', sealed, {
            params: { path: this.sealedPath(name) },
            headers: { 'Content-Type': 'application/octet-stream' },
          }),
        );
        count++;
        await this.load();
      }
      this.done.set(
        count === 1
          ? `„${files[0].name}“ verschlüsselt hochgeladen`
          : `${count} Dateien verschlüsselt hochgeladen`,
      );
    });
  }

  /** Downloads the ciphertext and decrypts it here. */
  protected async download(entry: VaultEntry): Promise<void> {
    const vault = this.service.vault();
    if (!vault) {
      return;
    }
    await this.run(async () => {
      this.done.set(`Entschlüssele „${entry.name}“ …`);
      const sealed = await firstValueFrom(
        this.http.get('/api/vault/file', {
          params: { path: this.sealedPath(entry.sealed) },
          responseType: 'blob',
        }),
      );
      const plain = await openFile(vault, sealed);
      const url = URL.createObjectURL(plain);
      const link = document.createElement('a');
      link.href = url;
      link.download = entry.name;
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 60_000);
      this.done.set(`„${entry.name}“ entschlüsselt`);
    });
  }

  protected async rename(entry: VaultEntry): Promise<void> {
    const vault = this.service.vault();
    const name = prompt('Neuer Name', entry.name)?.trim();
    if (!vault || !name || name === entry.name || !validName(name, this.error)) {
      return;
    }
    if (this.taken(name)) {
      this.error.set(`„${name}“ gibt es hier schon`);
      return;
    }
    await this.run(async () => {
      await firstValueFrom(
        this.http.post('/api/vault/move', {
          from: this.sealedPath(entry.sealed),
          to: this.sealedPath(await sealName(vault, name)),
        }),
      );
      this.done.set(`„${entry.name}“ heißt jetzt „${name}“`);
      await this.load();
    });
  }

  protected async remove(entry: VaultEntry): Promise<void> {
    const what = entry.dir ? `den Ordner „${entry.name}“ mit allem darin` : `„${entry.name}“`;
    if (!confirm(`${what} endgültig löschen? Im Tresor gibt es keinen Papierkorb.`)) {
      return;
    }
    await this.run(async () => {
      await firstValueFrom(
        this.http.post('/api/vault/delete', { path: this.sealedPath(entry.sealed) }),
      );
      this.done.set(`„${entry.name}“ gelöscht`);
      await this.load();
    });
  }

  protected async changePassphrase(
    event: Event,
    secret: string,
    passphrase: string,
    repeat: string,
  ): Promise<void> {
    event.preventDefault();
    const keys = this.keys();
    if (!keys || !this.checkPassphrase(passphrase, repeat)) {
      return;
    }
    await this.run(async () => {
      const changed = await changePassphrase(keys, this.secret(secret), passphrase);
      const stored = await firstValueFrom(
        this.http.post<{ keys: VaultKeys }>('/api/vault/keys', changed),
      );
      this.keys.set(stored.keys);
      this.recovered.set(false);
      (event.target as HTMLFormElement).reset();
      this.done.set('Neue Passphrase gespeichert');
    });
  }

  protected async renewRecovery(event: Event, secret: string): Promise<void> {
    event.preventDefault();
    const keys = this.keys();
    if (!keys) {
      return;
    }
    await this.run(async () => {
      this.pending.set(await newRecovery(keys, this.secret(secret)));
      (event.target as HTMLFormElement).reset();
    });
  }

  /** What opens the vault for a change: after a recovery, the recovery key. */
  private secret(text: string): Secret {
    return this.recovered() ? { recovery: text } : { passphrase: text };
  }

  private checkPassphrase(passphrase: string, repeat: string): boolean {
    if (passphrase.length < MIN_PASSPHRASE) {
      this.error.set(`Die Passphrase braucht mindestens ${MIN_PASSPHRASE} Zeichen`);
      return false;
    }
    if (passphrase !== repeat) {
      this.error.set('Die beiden Passphrasen sind nicht gleich');
      return false;
    }
    return true;
  }

  /** Runs one action, showing its error. */
  private async run(action: () => Promise<void>): Promise<void> {
    this.busy.set(true);
    this.error.set(null);
    this.done.set(null);
    this.service.touch();
    try {
      await action();
    } catch (err) {
      this.done.set(null);
      this.error.set(await failure(err));
    } finally {
      this.busy.set(false);
    }
  }
}

/** Whether name can be a name in the vault; sets the error otherwise. */
function validName(name: string, error: { set(v: string): void }): boolean {
  if (!name || name === '.' || name === '..' || /[/\\\0]/.test(name)) {
    error.set('Ungültiger Name');
    return false;
  }
  if (new TextEncoder().encode(name).length > 160) {
    error.set('Namen im Tresor dürfen höchstens 160 Bytes lang sein');
    return false;
  }
  return true;
}

/** The message for a failed action, also from an answer read as a blob. */
async function failure(err: unknown): Promise<string> {
  if (err instanceof HttpErrorResponse && err.error instanceof Blob) {
    try {
      const body = JSON.parse(await err.error.text()) as { error?: unknown };
      if (typeof body.error === 'string') {
        return body.error;
      }
    } catch {
      // Not JSON: fall through.
    }
  }
  if (err instanceof HttpErrorResponse) {
    return errorMessage(err);
  }
  return err instanceof Error ? err.message : 'Unbekannter Fehler';
}
