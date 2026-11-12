import { HttpClient } from '@angular/common/http';
import { Injectable, computed, inject, signal } from '@angular/core';
import type { Usage } from '../account/account';
import { errorMessage } from '../auth.service';
import { formatSize } from '../format';

export interface Disk {
  total: number;
  free: number;
  used: number;
  freePercent: number;
  /** "low" and "critical" warn. */
  level: 'ok' | 'low' | 'critical';
  warnPercent: number;
}

export interface RaidArray {
  name: string;
  level: string;
  active: boolean;
  disks: number;
  working: number;
  failed?: string[];
  /** Such as "resync" or "recovery", missing when idle. */
  sync?: string;
  progress?: number;
  healthy: boolean;
}

export interface StatusReport {
  version: string;
  started: string;
  /** When the VM started, missing when unknown. */
  booted?: string;
  disk: Disk | null;
  diskError?: string;
  /** Null when the server sees no RAID. */
  raid: RaidArray[] | null;
}

const percent = new Intl.NumberFormat('de-DE', { maximumFractionDigits: 1 });

/** What needs attention: low space, a broken mirror. Empty when all is well. */
export function warnings(report: StatusReport): string[] {
  const out: string[] = [];
  const disk = report.disk;
  if (disk && disk.level !== 'ok') {
    const left = `nur noch ${formatSize(disk.free)} frei (${percent.format(disk.freePercent)} %)`;
    out.push(
      disk.level === 'critical'
        ? `Speicher fast voll: ${left}. Bitte Dateien löschen und den Papierkorb leeren.`
        : `Wenig Speicherplatz: ${left}.`,
    );
  }
  for (const array of report.raid ?? []) {
    if (!array.active) {
      out.push(`RAID ${array.name} ist gestoppt.`);
    } else if (!array.healthy) {
      const failed = array.failed?.length ? ` Ausgefallen: ${array.failed.join(', ')}.` : '';
      out.push(
        `RAID ${array.name} läuft nur mit ${array.working} von ${array.disks} Platten.${failed}`,
      );
    }
  }
  return out;
}

/** From how many percent of the own quota on the banner warns. */
export const QUOTA_WARN_PERCENT = 90;

/** A warning when the account's own quota is almost or completely full, else null. */
export function quotaWarning(usage: Usage): string | null {
  if (usage.quota <= 0 || usage.used * 100 < usage.quota * QUOTA_WARN_PERCENT) {
    return null;
  }
  const taken = `${formatSize(usage.used)} von ${formatSize(usage.quota)} belegt`;
  return usage.used >= usage.quota
    ? `Dein Speicherplatz ist voll (${taken}). Bitte Dateien löschen und den Papierkorb leeren.`
    : `Dein Speicherplatz ist fast voll (${taken}).`;
}

/** The server's status, shared by the warning banner and the status page. */
@Injectable({ providedIn: 'root' })
export class StatusService {
  private readonly http = inject(HttpClient);

  /** The last report, null until the first one came. */
  readonly report = signal<StatusReport | null>(null);
  readonly error = signal<string | null>(null);
  readonly warnings = computed(() => {
    const report = this.report();
    return report ? warnings(report) : [];
  });
  /** The account's own usage, null until it was counted. */
  readonly usage = signal<Usage | null>(null);
  readonly quotaWarning = computed(() => {
    const usage = this.usage();
    return usage ? quotaWarning(usage) : null;
  });

  refresh(): void {
    this.http.get<StatusReport>('/api/status').subscribe({
      next: (report) => {
        this.report.set(report);
        this.error.set(null);
      },
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  /** Counts the account's own usage again, for the quota warning. */
  refreshUsage(): void {
    this.http.get<Usage>('/api/usage').subscribe({
      next: (usage) => this.usage.set(usage),
      // The warning stays as it was; the account page says what failed.
      error: () => undefined,
    });
  }
}
