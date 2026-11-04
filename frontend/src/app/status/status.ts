import { Component, inject } from '@angular/core';
import { formatDuration, formatSize } from '../format';
import { RaidArray, StatusService } from './status.service';

const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium', timeStyle: 'short' });
const percent = new Intl.NumberFormat('de-DE', { maximumFractionDigits: 1 });

const syncNames: Record<string, string> = {
  resync: 'Abgleich',
  recovery: 'Wiederherstellung',
  reshape: 'Umbau',
  check: 'Prüfung',
  repair: 'Reparatur',
};

/** The status page: free space, the RAID and how long everything is up. */
@Component({
  selector: 'app-status',
  templateUrl: './status.html',
  styleUrl: './status.css',
})
export class StatusView {
  protected readonly status = inject(StatusService);
  protected readonly formatSize = formatSize;
  protected readonly percent = (n: number) => percent.format(n);

  constructor() {
    this.status.refresh();
  }

  /** "seit 04.11.2026, 10:00 (3 Tage, 4 Std.)". */
  protected since(time: string): string {
    const start = new Date(time);
    return `seit ${date.format(start)} (${formatDuration(Date.now() - start.getTime())})`;
  }

  protected raidState(array: RaidArray): string {
    if (!array.active) {
      return 'Gestoppt';
    }
    let state = array.healthy
      ? `In Ordnung, ${array.working} von ${array.disks} Platten`
      : `Beschädigt, nur ${array.working} von ${array.disks} Platten`;
    if (array.sync) {
      const name = syncNames[array.sync] ?? array.sync;
      state += `; ${name} ${percent.format(array.progress ?? 0)} %`;
    }
    return state;
  }
}
