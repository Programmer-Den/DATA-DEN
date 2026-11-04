import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { AppPassword, TokensView, formatDay } from './tokens';

const SCRIPT: AppPassword = {
  id: 'a1b2',
  name: 'Backup-Skript',
  readOnly: true,
  created: '2026-11-04T12:00:00Z',
  expires: null,
  lastUsed: null,
};

describe('TokensView', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    http.verify();
    vi.restoreAllMocks();
  });

  async function start(tokens: AppPassword[]): Promise<ComponentFixture<TokensView>> {
    const fixture = TestBed.createComponent(TokensView);
    http.expectOne('/api/account/tokens').flush({ tokens });
    await fixture.whenStable();
    return fixture;
  }

  function page(fixture: ComponentFixture<TokensView>): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  it('lists the app passwords', async () => {
    const fixture = await start([SCRIPT]);
    const row = page(fixture).querySelector('tbody tr')!.textContent!;
    expect(row).toContain('Backup-Skript');
    expect(row).toContain('nur lesen');
    expect(row).toContain('nie');
    expect(page(fixture).querySelector('.dav')?.textContent).toBe(`${location.origin}/dav/`);
  });

  it('creates one and shows it once', async () => {
    const fixture = await start([]);
    expect(page(fixture).textContent).toContain('Noch keine');
    const form = page(fixture).querySelector('form')!;
    form.querySelector<HTMLInputElement>('[name=name]')!.value = ' Laptop ';
    form.querySelector<HTMLInputElement>('[name=readOnly]')!.checked = true;
    form.querySelector<HTMLInputElement>('[name=expiresDays]')!.value = '30';
    form.querySelector<HTMLInputElement>('[name=password]')!.value = 'richtig';
    form.dispatchEvent(new Event('submit'));
    const req = http.expectOne('/api/account/tokens');
    expect(req.request.method).toBe('POST');
    expect(req.request.body).toEqual({
      name: 'Laptop',
      password: 'richtig',
      readOnly: true,
      expiresDays: 30,
    });
    req.flush({ token: 'dd_geheim', info: { ...SCRIPT, name: 'Laptop' } });
    http.expectOne('/api/account/tokens').flush({ tokens: [{ ...SCRIPT, name: 'Laptop' }] });
    await fixture.whenStable();
    expect(page(fixture).querySelector('.secret')?.textContent).toBe('dd_geheim');
  });

  it('shows why creating failed', async () => {
    const fixture = await start([]);
    page(fixture).querySelector('form')!.dispatchEvent(new Event('submit'));
    http
      .expectOne('/api/account/tokens')
      .flush({ error: 'Das Passwort stimmt nicht' }, { status: 403, statusText: 'Forbidden' });
    await fixture.whenStable();
    expect(page(fixture).querySelector('[role=alert]')?.textContent).toContain('stimmt nicht');
  });

  it('revokes one after asking', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const fixture = await start([SCRIPT]);
    page(fixture).querySelector<HTMLButtonElement>('tbody button')!.click();
    http.expectOne('/api/account/tokens/a1b2/delete').flush({});
    http.expectOne('/api/account/tokens').flush({ tokens: [] });
    await fixture.whenStable();
    expect(page(fixture).querySelector('table')).toBeNull();
  });

  it('formats days', () => {
    expect(formatDay(null)).toBe('–');
    expect(formatDay('2026-11-04T12:00:00Z')).toBe('4.11.2026');
  });
});
