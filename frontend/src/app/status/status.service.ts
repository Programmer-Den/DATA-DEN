import { HttpClient } from '@angular/common/http';
import { Injectable, computed, inject, signal } from '@angular/core';
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

  refresh(): void {
    this.http.get<StatusReport>('/api/status').subscribe({
      next: (report) => {
        this.report.set(report);
        this.error.set(null);
      },
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }
}
