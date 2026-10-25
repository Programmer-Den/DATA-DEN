import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { App } from './app';

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
  async function start(session: string | null): Promise<ComponentFixture<App>> {
    const fixture = TestBed.createComponent(App);
    http.expectOne('/api/health').flush({ status: 'ok', version: '1.2.3' });
    const req = http.expectOne('/api/session');
    if (session) {
      req.flush({ username: session });
      await fixture.whenStable();
      http.expectOne('/api/files?path=/').flush({ path: '/', entries: [] });
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
    await fixture.whenStable();
    expect(page(fixture).textContent).toContain('Angemeldet als chriszly');
    expect(page(fixture).textContent).toContain('DATA-DEN 1.2.3');
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
});
