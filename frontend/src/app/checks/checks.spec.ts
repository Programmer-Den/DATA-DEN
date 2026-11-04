import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { ChecksView, Duplicates, Integrity, POLL_MS } from './checks';

const healthy: Integrity = {
  files: 120,
  running: false,
  done: 0,
  total: 0,
  lastCheck: '2026-11-04T10:00:00Z',
  verifyDays: 30,
  damaged: [],
};
const damaged: Integrity = {
  ...healthy,
  damaged: [
    {
      path: '/Fotos/strand.jpg',
      size: 2048,
      expected: 'aa',
      actual: 'bb',
      found: '2026-11-03T08:00:00Z',
    },
  ],
};
const duplicates: Duplicates = {
  groups: [{ sha256: 'cc', size: 1024, paths: ['/Fotos/a.jpg', '/Alt/a.jpg'] }],
  wasted: 1024,
};

describe('ChecksView', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<ChecksView>;

  async function start(state: Integrity, dups: Duplicates = duplicates): Promise<void> {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    fixture = TestBed.createComponent(ChecksView);
    http.expectOne('/api/integrity').flush(state);
    http.expectOne('/api/duplicates').flush(dups);
    await fixture.whenStable();
  }

  afterEach(() => {
    http.verify();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  function button(text: string): HTMLButtonElement {
    return [...page().querySelectorAll('button')].find((b) => b.textContent?.includes(text))!;
  }

  async function reload(state: Integrity, dups: Duplicates = duplicates): Promise<void> {
    http.expectOne('/api/integrity').flush(state);
    http.expectOne('/api/duplicates').flush(dups);
    await fixture.whenStable();
  }

  it('says when all files are fine and lists the duplicates', async () => {
    await start(healthy);
    expect(page().textContent).toContain('120 Dateien mit Prüfsumme');
    expect(page().textContent).toContain('Keine beschädigten Dateien');
    expect(page().textContent).toContain('spart 1 KB');
    const paths = [...page().querySelectorAll('.group li span')].map((s) => s.textContent);
    expect(paths).toEqual(['/Fotos/a.jpg', '/Alt/a.jpg']);
  });

  it('says when there are no duplicates', async () => {
    await start(healthy, { groups: [], wasted: 0 });
    expect(page().textContent).toContain('Keine Dateien mit gleichem Inhalt');
  });

  it('lists damaged files and accepts one', async () => {
    await start(damaged);
    const row = page().querySelector('tr.damaged')!;
    expect(row.textContent).toContain('/Fotos/strand.jpg');
    expect(row.textContent).toContain('2 KB');
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    button('Als richtig übernehmen').click();
    const req = http.expectOne('/api/integrity/accept');
    expect(req.request.body).toEqual({ path: '/Fotos/strand.jpg' });
    req.flush({ path: '/Fotos/strand.jpg' });
    await reload(healthy);
    expect(page().querySelector('[role=status]')?.textContent).toContain('übernommen');
    expect(page().querySelector('tr.damaged')).toBeNull();
  });

  it('does not accept without asking', async () => {
    await start(damaged);
    vi.spyOn(window, 'confirm').mockReturnValue(false);
    button('Als richtig übernehmen').click();
    http.expectNone('/api/integrity/accept');
  });

  it('moves a duplicate into the recycle bin', async () => {
    await start(healthy);
    page().querySelector<HTMLButtonElement>('[aria-label="/Alt/a.jpg löschen"]')!.click();
    const req = http.expectOne('/api/delete');
    expect(req.request.body).toEqual({ path: '/Alt/a.jpg' });
    req.flush({ path: '/Alt/a.jpg', id: 'x' });
    await reload(healthy, { groups: [], wasted: 0 });
    expect(page().textContent).toContain('Papierkorb verschoben');
  });

  it('starts a check of all files and follows its progress', async () => {
    await start(healthy);
    // Angular waits for timers itself, so these steps render by hand.
    vi.useFakeTimers();
    const flush = (state: Integrity) => {
      http.expectOne('/api/integrity').flush(state);
      http.expectOne('/api/duplicates').flush(duplicates);
      fixture.detectChanges();
    };
    button('Jetzt alle Dateien prüfen').click();
    http.expectOne('/api/integrity/check').flush({}, { status: 202, statusText: 'Accepted' });
    vi.advanceTimersByTime(500);
    flush({ ...healthy, running: true, done: 30, total: 120 });
    expect(page().querySelector('.running')?.textContent).toContain('30 von 120');
    expect(button('Jetzt alle Dateien prüfen').disabled).toBe(true);
    vi.advanceTimersByTime(POLL_MS);
    flush(healthy);
    expect(page().querySelector('.running')).toBeNull();
  });

  it("shows the server's message", async () => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    fixture = TestBed.createComponent(ChecksView);
    http
      .expectOne('/api/integrity')
      .flush(
        { error: 'Datenordner nicht verfügbar' },
        { status: 503, statusText: 'Service Unavailable' },
      );
    http.expectOne('/api/duplicates').flush({ error: 'x' }, { status: 503, statusText: 'x' });
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toBeTruthy();
  });
});
