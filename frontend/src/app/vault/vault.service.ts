import { Injectable, effect, inject, signal } from '@angular/core';
import { AuthService } from '../auth.service';
import { Vault } from './vault-crypto';

/** How long the vault stays open without anything done in it. */
export const IDLE_LOCK = 15 * 60 * 1000;

/**
 * The open vault, kept only in this page's memory: switching tabs keeps it
 * open, reloading the page, logging out or 15 minutes without use lock it.
 */
@Injectable({ providedIn: 'root' })
export class VaultService {
  readonly vault = signal<Vault | null>(null);
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor() {
    const auth = inject(AuthService);
    effect(() => {
      if (!auth.user()) {
        this.lock();
      }
    });
  }

  open(vault: Vault): void {
    this.vault.set(vault);
    this.touch();
  }

  /** Something was done in the vault: the idle time starts again. */
  touch(): void {
    clearTimeout(this.timer);
    if (this.vault()) {
      this.timer = setTimeout(() => this.lock(), IDLE_LOCK);
    }
  }

  lock(): void {
    clearTimeout(this.timer);
    this.vault.set(null);
  }
}
