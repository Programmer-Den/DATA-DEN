import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { App, shareIdOf } from './app';
import { StatusReport } from './status/status.service';

const fine: StatusReport = {
  version: '1.2.3',
  started: '2026-11-04T10:00:00Z',
  disk: { total: 4e12, free: 3e12, used: 1e12, freePercent: 75, level: 'ok', warnPercent: 10 },
  raid: [{ name: 'md127', level: 'raid1', active: true, disks: 2, working: 2, healthy: true }],
};

describe('App', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => http.verify());

  /** Starts the app; session null means nobody is logged in. */
  async function start(
    session: string | null,
    status: StatusReport = fine,
    admin = false,
  ): Promise<ComponentFixture<App>> {
    const fixture = TestBed.createComponent(App);
    http.expectOne('/api/health').flush({ status: 'ok', version: '1.2.3' });
    const req = http.expectOne('/api/session');
    if (session) {
      req.flush({ username: session, admin });
      await fixture.whenStable();
      http.expectOne('/api/files?path=/').flush({ path: '/', entries: [] });
      http.expectOne('/api/status').flush(status);
    } else {
      req.flush({ error: 'Nicht angemeldet' }, { status: 401, statusText: 'Unauthorized' });
    }
    await fixture.whenStable();
    return fixture;
  }

  function page(fixture: ComponentFixture<App>): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  async function submit(fixture: ComponentFixture<App>, username: string, password: string) {
    const el = page(fixture);
    el.querySelector<HTMLInputElement>('input[name=username]')!.value = username;
    el.querySelector<HTMLInputElement>('input[name=password]')!.value = password;
    el.querySelector('form')!.dispatchEvent(new Event('submit'));
    await fixture.whenStable();
  }

  it('shows the title', () => {
    const fixture = TestBed.createComponent(App);
    fixture.detectChanges();
    http.expectOne('/api/health');
    http.expectOne('/api/session');
    expect(page(fixture).querySelector('h1')?.textContent).toBe('DATA-DEN');
  });

  it('says when the server cannot be reached', async () => {
    const fixture = TestBed.createComponent(App);
    http.expectOne('/api/health').flush('', { status: 502, statusText: 'Bad Gateway' });
    http.expectOne('/api/session').flush('', { status: 502, statusText: 'Bad Gateway' });
    await fixture.whenStable();
    expect(page(fixture).textContent).toContain('nicht erreichbar');
  });

  it('asks to log in when there is no session', async () => {
    const fixture = await start(null);
    expect(page(fixture).querySelector('form')).not.toBeNull();
    expect(page(fixture).textContent).not.toContain('DATA-DEN 1.2.3');
  });

  it('logs in', async () => {
    const fixture = await start(null);
    await submit(fixture, 'chriszly', 'richtig');
    const req = http.expectOne('/api/login');
    expect(req.request.body).toEqual({ username: 'chriszly', password: 'richtig' });
    req.flush({ username: 'chriszly' });
    await fixture.whenStable();
    http.expectOne('/api/files?path=/').flush({ path: '/', entries: [] });
    http.expectOne('/api/status').flush(fine);
    await fixture.whenStable();
    expect(page(fixture).textContent).toContain('Angemeldet als chriszly');
    expect(page(fixture).textContent).toContain('DATA-DEN 1.2.3');
  });

  it('asks for the code of the second factor', async () => {
    const fixture = await start(null);
    await submit(fixture, 'chriszly', 'richtig');
    http
      .expectOne('/api/login')
      .flush(
        { error: 'Bitte den Code aus der App eingeben', twoFactor: true },
        { status: 401, statusText: 'Unauthorized' },
      );
    await fixture.whenStable();
    const code = page(fixture).querySelector<HTMLInputElement>('input[name=code]')!;
    expect(code).not.toBeNull();
    code.value = '123456';
    await submit(fixture, 'chriszly', 'richtig');
    const req = http.expectOne('/api/login');
    expect(req.request.body).toEqual({ username: 'chriszly', password: 'richtig', code: '123456' });
    req.flush({ username: 'chriszly', twoFactor: true });
    await fixture.whenStable();
    http.expectOne('/api/files?path=/').flush({ path: '/', entries: [] });
    http.expectOne('/api/status').flush(fine);
    await fixture.whenStable();
    expect(page(fixture).textContent).toContain('Angemeldet als chriszly');
  });

  it("shows the server's message when the login fails", async () => {
    const fixture = await start(null);
    await submit(fixture, 'chriszly', 'falsch');
    http
      .expectOne('/api/login')
      .flush(
        { error: 'Benutzername oder Passwort falsch' },
        { status: 401, statusText: 'Unauthorized' },
      );
    await fixture.whenStable();
    expect(page(fixture).querySelector('[role=alert]')?.textContent).toContain('Passwort falsch');
    expect(page(fixture).querySelector('form')).not.toBeNull();
  });

  it('logs out', async () => {
    const fixture = await start('chriszly');
    page(fixture).querySelector<HTMLButtonElement>('button')!.click();
    http.expectOne('/api/logout').flush(null, { status: 204, statusText: 'No Content' });
    await fixture.whenStable();
    expect(page(fixture).querySelector('form')).not.toBeNull();
  });

  it('switches between the views', async () => {
    const fixture = await start('chriszly');
    const tab = (name: string) =>
      [...page(fixture).querySelectorAll<HTMLButtonElement>('.tabs button')].find(
        (b) => b.textContent?.trim() === name,
      )!;
    expect(tab('Dateien').getAttribute('aria-current')).toBe('page');

    tab('Fotos').click();
    await fixture.whenStable();
    http.expectOne('/api/media?kind=photo').flush({ kind: 'photo', items: [] });
    http.expectOne('/api/albums').flush({ albums: [] });
    await fixture.whenStable();
    expect(page(fixture).textContent).toContain('Keine Fotos gefunden');

    tab('Dokumente').click();
    await fixture.whenStable();
    http.expectOne('/api/media?kind=document').flush({ kind: 'document', items: [] });
    await fixture.whenStable();
    expect(tab('Dokumente').getAttribute('aria-current')).toBe('page');

    tab('Dateien').click();
    await fixture.whenStable();
    http.expectOne('/api/files?path=/').flush({ path: '/', entries: [] });
  });

  it('warns about low space and a broken mirror, and links to the status page', async () => {
    const fixture = await start('chriszly', {
      ...fine,
      disk: { ...fine.disk!, free: 2e11, freePercent: 5, level: 'low' },
      raid: [{ ...fine.raid![0], working: 1, failed: ['sdc'], healthy: false }],
    });
    const banner = page(fixture).querySelector('.warnings')!;
    expect(banner.textContent).toContain('Wenig Speicherplatz');
    expect(banner.textContent).toContain('md127 läuft nur mit 1 von 2 Platten');

    banner.querySelector('button')!.click();
    await fixture.whenStable();
    http.expectOne('/api/status').flush(fine);
    await fixture.whenStable();
    expect(page(fixture).querySelector('.warnings')).toBeNull();
    expect(page(fixture).textContent).toContain('Alles in Ordnung');
  });

  it('shows no warning when all is well', async () => {
    const fixture = await start('chriszly');
    expect(page(fixture).querySelector('.warnings')).toBeNull();
  });

  it('shows the accounts only to admins', async () => {
    const names = (fixture: ComponentFixture<App>) =>
      [...page(fixture).querySelectorAll('.tabs button')].map((b) => b.textContent?.trim());
    const user = await start('anna');
    expect(names(user)).toContain('Konto');
    expect(names(user)).not.toContain('Benutzer');
    user.destroy();

    const admin = await start('chriszly', fine, true);
    expect(names(admin)).toContain('Benutzer');
    [...page(admin).querySelectorAll<HTMLButtonElement>('.tabs button')]
      .find((b) => b.textContent?.trim() === 'Benutzer')!
      .click();
    await admin.whenStable();
    http.expectOne('/api/users').flush({ users: [] });
  });
});

describe('shareIdOf', () => {
  it('finds the id of a share link', () => {
    const id = '0123456789abcdef0123456789abcdef';
    expect(shareIdOf('/s/' + id)).toBe(id);
    expect(shareIdOf('/s/' + id + '/')).toBe(id);
    expect(shareIdOf('/')).toBeNull();
    expect(shareIdOf('/s/kurz')).toBeNull();
    expect(shareIdOf('/s/' + id + '/mehr')).toBeNull();
  });
});
