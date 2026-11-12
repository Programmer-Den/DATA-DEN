import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { AuthService } from '../auth.service';
import { AuditEntry, AuditView } from './audit';

const entries: AuditEntry[] = [
  { time: '2026-11-04T12:00:02Z', user: 'anna', ip: '192.0.2.9', action: 'login-failed' },
  { time: '2026-11-04T12:00:01Z', user: 'anna', action: 'upload', target: '/Fotos/a.jpg' },
  { time: '2026-11-04T12:00:00Z', user: 'anna', action: 'something-new' },
];

describe('AuditView', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => http.verify());

  async function start(admin: boolean): Promise<ComponentFixture<AuditView>> {
    TestBed.inject(AuthService).admin.set(admin);
    const fixture = TestBed.createComponent(AuditView);
    http.expectOne('/api/audit').flush({ entries });
    await fixture.whenStable();
    return fixture;
  }

  function page(fixture: ComponentFixture<AuditView>): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  it('lists the entries in German', async () => {
    const fixture = await start(false);
    const rows = page(fixture).querySelectorAll('tbody tr');
    expect(rows.length).toBe(3);
    expect(rows[0].textContent).toContain('Anmeldung fehlgeschlagen');
    expect(rows[0].textContent).toContain('192.0.2.9');
    expect(rows[0].classList).toContain('failed');
    expect(rows[1].textContent).toContain('Hochgeladen');
    expect(rows[1].textContent).toContain('/Fotos/a.jpg');
    expect(rows[2].textContent).toContain('something-new');
    expect(page(fixture).querySelector('input[name=user]')).toBeNull();
    expect(page(fixture).querySelector('.older')).toBeNull();
  });

  it('filters by kind and file', async () => {
    const fixture = await start(false);
    page(fixture).querySelector<HTMLSelectElement>('select[name=kind]')!.selectedIndex = 2;
    page(fixture).querySelector<HTMLInputElement>('input[name=target]')!.value = ' Fotos ';
    page(fixture).querySelector('form')!.dispatchEvent(new Event('submit'));
    http
      .expectOne('/api/audit?action=delete,restore,purge,trash-empty,version-delete&target=Fotos')
      .flush({ entries: [] });
    await fixture.whenStable();
    expect(page(fixture).textContent).toContain('Noch keine Einträge');
  });

  it('loads older entries with the same filter', async () => {
    const fixture = await start(true);
    page(fixture).querySelector<HTMLInputElement>('input[name=user]')!.value = 'anna';
    page(fixture).querySelector('form')!.dispatchEvent(new Event('submit'));
    const full = Array.from({ length: 200 }, (_, i) => ({
      ...entries[1],
      time: new Date(Date.UTC(2026, 10, 4, 12, 0, 200 - i)).toISOString(),
    }));
    http.expectOne('/api/audit?user=anna').flush({ entries: full });
    await fixture.whenStable();
    page(fixture).querySelector<HTMLButtonElement>('.older')!.click();
    http
      .expectOne(`/api/audit?user=anna&before=${full[199].time}`)
      .flush({ entries: [entries[0]] });
    await fixture.whenStable();
    expect(page(fixture).querySelectorAll('tbody tr').length).toBe(201);
    expect(page(fixture).querySelector('.older')).toBeNull();
  });

  it('lets admins filter by user', async () => {
    const fixture = await start(true);
    page(fixture).querySelector<HTMLInputElement>('input[name=user]')!.value = ' bob ';
    page(fixture).querySelector('form')!.dispatchEvent(new Event('submit'));
    http.expectOne('/api/audit?user=bob').flush({ entries: [] });
    await fixture.whenStable();
    expect(page(fixture).textContent).toContain('Noch keine Einträge');
  });
});
