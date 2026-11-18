import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { Checks, SetupView, checkList } from './setup';

const fine: Checks = {
  dataDir: '/data',
  writable: true,
  disk: { total: 4e12, free: 3e12, used: 1e12, freePercent: 75, level: 'ok', warnPercent: 10 },
  raid: [{ name: 'md127', level: 'raid1', active: true, disks: 2, working: 2, healthy: true }],
  https: true,
  trustedProxy: '192.168.1.20',
};

describe('checkList', () => {
  it('is all fine for a finished installation', () => {
    const list = checkList(fine, 'https://nas.example.de');
    expect(list.every((c) => c.ok)).toBe(true);
    expect(list.map((c) => c.text)).toContain('Erreichbar unter nas.example.de');
    expect(list.map((c) => c.text)).toContain('Der Pi hat die Adresse 192.168.1.20');
  });

  it('warns about what is missing, with what to do', () => {
    const list = checkList(
      {
        ...fine,
        writable: false,
        writeError: 'permission denied',
        raid: null,
        https: false,
        trustedProxy: '',
      },
      'http://192.168.1.30:8080',
    );
    const warnings = list.filter((c) => !c.ok);
    expect(warnings.map((c) => c.text)).toEqual([
      'In den Datenordner /data kann nicht geschrieben werden: permission denied',
      'Kein RAID gefunden: die Daten liegen nur auf einer Platte',
      'Der Weg vom Pi zur VM ist nicht verschlüsselt',
      'Die Adresse des Pi ist nicht eingetragen, so zählen Fehlversuche aller Besucher zusammen',
      'Diese Seite ist ohne HTTPS geöffnet (192.168.1.30:8080)',
    ]);
    expect(warnings.every((c) => c.hint)).toBe(true);
  });

  it('shows a mirror that is still being synced as fine', () => {
    const syncing = { ...fine.raid![0], sync: 'resync', progress: 12.5 };
    const list = checkList({ ...fine, raid: [syncing] }, 'https://nas.example.de');
    expect(list[2]).toEqual({
      ok: true,
      text: 'RAID md127 (raid1): 2 von 2 Platten, Abgleich 12,5 % (die Platten sind schon nutzbar)',
    });
  });

  it('takes localhost without HTTPS', () => {
    expect(checkList(fine, 'http://localhost:4200').at(-1)?.ok).toBe(true);
  });
});

describe('SetupView', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => http.verify());

  function page(fixture: ComponentFixture<SetupView>): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  async function fill(fixture: ComponentFixture<SetupView>, values: Record<string, string>) {
    for (const [name, value] of Object.entries(values)) {
      page(fixture).querySelector<HTMLInputElement>(`input[name=${name}]`)!.value = value;
    }
    page(fixture).querySelector('form')!.dispatchEvent(new Event('submit'));
    await fixture.whenStable();
  }

  function button(fixture: ComponentFixture<SetupView>, text: string): HTMLButtonElement {
    return [...page(fixture).querySelectorAll('button')].find((b) =>
      b.textContent?.includes(text),
    )!;
  }

  async function toChecks(fixture: ComponentFixture<SetupView>, checks: Checks) {
    await fixture.whenStable();
    await fill(fixture, { code: ' ABCD-EFGH-JKLM ' });
    const req = http.expectOne('/api/setup/check');
    expect(req.request.body).toEqual({ code: 'ABCD-EFGH-JKLM' });
    req.flush(checks);
    await fixture.whenStable();
  }

  it('says when the code is wrong', async () => {
    const fixture = TestBed.createComponent(SetupView);
    await fixture.whenStable();
    await fill(fixture, { code: 'falsch' });
    http
      .expectOne('/api/setup/check')
      .flush(
        { error: 'Der Einrichtungscode stimmt nicht' },
        { status: 403, statusText: 'Forbidden' },
      );
    await fixture.whenStable();
    expect(page(fixture).querySelector('[role=alert]')?.textContent).toContain(
      'Einrichtungscode stimmt nicht',
    );
  });

  it('checks, then creates the account', async () => {
    const fixture = TestBed.createComponent(SetupView);
    let done = '';
    fixture.componentInstance.done.subscribe((name) => (done = name));
    await toChecks(fixture, fine);
    expect(page(fixture).textContent).toContain('Datenordner /data ist beschreibbar');

    button(fixture, 'Weiter').click();
    await fixture.whenStable();
    await fill(fixture, {
      username: 'chriszly',
      password: 'ein-langes-passwort',
      repeat: 'ein-anderes-passwort',
    });
    expect(page(fixture).querySelector('[role=alert]')?.textContent).toContain('verschieden');

    await fill(fixture, { repeat: 'ein-langes-passwort' });
    const req = http.expectOne('/api/setup');
    expect(req.request.body).toEqual({
      code: 'ABCD-EFGH-JKLM',
      username: 'chriszly',
      password: 'ein-langes-passwort',
    });
    req.flush({ username: 'chriszly' }, { status: 201, statusText: 'Created' });
    await fixture.whenStable();
    expect(done).toBe('chriszly');
  });

  it('goes on only with a writable data folder, and checks again', async () => {
    const fixture = TestBed.createComponent(SetupView);
    await toChecks(fixture, { ...fine, writable: false, writeError: 'permission denied' });
    expect(button(fixture, 'Weiter').disabled).toBe(true);
    expect(page(fixture).textContent).toContain('sudo chown -R 65532:65532 /srv/data-den');

    button(fixture, 'Erneut prüfen').click();
    const req = http.expectOne('/api/setup/check');
    expect(req.request.body).toEqual({ code: 'ABCD-EFGH-JKLM' });
    req.flush(fine);
    await fixture.whenStable();
    expect(button(fixture, 'Weiter').disabled).toBe(false);
  });
});
