import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { StatusView } from './status';
import { StatusReport, quotaWarning, warnings } from './status.service';

const report: StatusReport = {
  version: '1.2.3',
  started: '2026-11-04T10:00:00Z',
  booted: '2026-11-01T08:00:00Z',
  disk: {
    total: 4 * 1024 ** 4,
    free: 3 * 1024 ** 4,
    used: 1024 ** 4,
    freePercent: 75,
    level: 'ok',
    warnPercent: 10,
  },
  raid: [
    {
      name: 'md127',
      level: 'raid1',
      active: true,
      disks: 2,
      working: 2,
      sync: 'resync',
      progress: 8.5,
      healthy: true,
    },
  ],
};

describe('warnings', () => {
  it('is empty when all is well', () => {
    expect(warnings(report)).toEqual([]);
  });

  it('warns about low and almost no space', () => {
    const low = { ...report.disk!, free: 1024 ** 4 / 4, freePercent: 6.25, level: 'low' as const };
    expect(warnings({ ...report, disk: low })).toEqual([
      'Wenig Speicherplatz: nur noch 256 GB frei (6,3 %).',
    ]);
    const critical = { ...low, level: 'critical' as const };
    expect(warnings({ ...report, disk: critical })[0]).toContain('Speicher fast voll');
  });

  it('warns about a broken or stopped mirror', () => {
    const broken = { ...report.raid![0], working: 1, failed: ['sdc'], healthy: false };
    const stopped = { ...broken, name: 'md2', active: false };
    expect(warnings({ ...report, raid: [broken, stopped] })).toEqual([
      'RAID md127 läuft nur mit 1 von 2 Platten. Ausgefallen: sdc.',
      'RAID md2 ist gestoppt.',
    ]);
  });
});

describe('quotaWarning', () => {
  it('stays quiet without a limit and below 90 %', () => {
    expect(quotaWarning({ used: 5e12, quota: 0 })).toBeNull();
    expect(quotaWarning({ used: 8.9 * 1024 ** 3, quota: 10 * 1024 ** 3 })).toBeNull();
  });

  it('warns from 90 % and when full', () => {
    expect(quotaWarning({ used: 9 * 1024 ** 3, quota: 10 * 1024 ** 3 })).toBe(
      'Dein Speicherplatz ist fast voll (9 GB von 10 GB belegt).',
    );
    expect(quotaWarning({ used: 1e10, quota: 1e10 })).toContain('Dein Speicherplatz ist voll');
  });
});

describe('StatusView', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<StatusView>;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    fixture = TestBed.createComponent(StatusView);
  });

  afterEach(() => http.verify());

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  it('shows space, the RAID and the uptime', async () => {
    http.expectOne('/api/status').flush(report);
    await fixture.whenStable();
    const text = page().textContent!;
    expect(text).toContain('Alles in Ordnung');
    expect(text).toContain('3 TB frei von 4 TB');
    expect(text).toContain('In Ordnung, 2 von 2 Platten; Abgleich 8,5 %');
    expect(text).toContain('1.2.3');
    expect(text).toContain('VM läuft');
    expect(page().querySelector('meter')?.getAttribute('value')).toBe('25');
  });

  it('lists the warnings', async () => {
    http
      .expectOne('/api/status')
      .flush({ ...report, raid: [{ ...report.raid![0], active: false }] });
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toContain('RAID md127 ist gestoppt');
    expect(page().textContent).not.toContain('Alles in Ordnung');
  });

  it('says when there is no RAID or the space is unknown', async () => {
    http
      .expectOne('/api/status')
      .flush({ ...report, disk: null, diskError: 'Speicherplatz nicht lesbar', raid: null });
    await fixture.whenStable();
    expect(page().textContent).toContain('Speicherplatz nicht lesbar');
    expect(page().textContent).toContain('Kein RAID gefunden');
  });

  it('loads again', async () => {
    http.expectOne('/api/status').flush(report);
    await fixture.whenStable();
    page().querySelector<HTMLButtonElement>('button')!.click();
    http.expectOne('/api/status').flush({ ...report, version: '1.2.4' });
    await fixture.whenStable();
    expect(page().textContent).toContain('1.2.4');
  });

  it("shows the server's error", async () => {
    http
      .expectOne('/api/status')
      .flush({ error: 'Nicht angemeldet' }, { status: 401, statusText: 'Unauthorized' });
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toContain('Nicht angemeldet');
  });
});
