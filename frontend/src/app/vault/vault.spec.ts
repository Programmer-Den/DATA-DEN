import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { VaultView } from './vault';
import { VaultService } from './vault.service';
import { VaultKeys, createVault, openFile, sealFile, sealName } from './vault-crypto';

const PASSPHRASE = 'eine lange passphrase';

describe('VaultView', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<VaultView>;
  let made: Awaited<ReturnType<typeof createVault>>;

  beforeAll(async () => {
    made = await createVault(PASSPHRASE, 100_000);
  });

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  const objectUrls = { create: URL.createObjectURL, revoke: URL.revokeObjectURL };

  afterEach(() => {
    URL.createObjectURL = objectUrls.create;
    URL.revokeObjectURL = objectUrls.revoke;
    TestBed.inject(VaultService).lock();
    http.verify();
    vi.restoreAllMocks();
  });

  async function start(keys: VaultKeys | null): Promise<void> {
    fixture = TestBed.createComponent(VaultView);
    http.expectOne('/api/vault').flush({ keys });
    await fixture.whenStable();
  }

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  function field(name: string): HTMLInputElement {
    return page().querySelector<HTMLInputElement>(`input[name=${name}]`)!;
  }

  function submit(selector = 'form'): void {
    page().querySelector<HTMLFormElement>(selector)!.dispatchEvent(new Event('submit'));
  }

  /** Waits for the request to url, as the crypto runs in between. */
  async function request(url: string) {
    return vi.waitFor(() => http.expectOne((r) => r.url === url), { timeout: 5000 });
  }

  async function shown(text: string): Promise<void> {
    await vi.waitFor(
      () => {
        fixture.detectChanges();
        expect(page().textContent).toContain(text);
      },
      { timeout: 5000 },
    );
  }

  async function unlocked(): Promise<void> {
    await start(made.keys);
    field('passphrase').value = PASSPHRASE;
    submit();
  }

  it('sets up a vault, showing the recovery key before storing the keys', async () => {
    await start(null);
    expect(page().textContent).toContain('Ohne Passphrase und Wiederherstellungsschlüssel');

    field('passphrase').value = 'kurz';
    field('repeat').value = 'kurz';
    submit();
    await shown('mindestens 12 Zeichen');

    field('passphrase').value = PASSPHRASE;
    field('repeat').value = PASSPHRASE + '!';
    submit();
    await shown('nicht gleich');

    field('repeat').value = PASSPHRASE;
    submit();
    await shown('sicher aufbewahrt');
    const key = page().querySelector('.key code')!.textContent!;
    expect(key).toMatch(/^([0-9A-Z]{4}-){12}[0-9A-Z]{4}$/);
    http.expectNone('/api/vault/keys');

    field('noted').checked = true;
    submit();
    const req = await request('/api/vault/keys');
    const body = req.request.body as { keys: VaultKeys; proof: string };
    expect(JSON.stringify(body)).not.toContain(PASSPHRASE);
    expect(body.keys.passphrase.iterations).toBe(600_000);
    expect(body.proof).toMatch(/^[A-Za-z0-9+/]{43}=$/);
    req.flush({ keys: body.keys });
    (await request('/api/vault/files')).flush({ entries: [] });
    await shown('Dieser Ordner ist leer');
  });

  it('refuses a wrong passphrase and lists the names it opens', async () => {
    await start(made.keys);
    field('passphrase').value = 'falsch falsch falsch';
    submit();
    await shown('stimmt nicht');

    field('passphrase').value = PASSPHRASE;
    submit();
    const req = await request('/api/vault/files');
    expect(req.request.params.get('path')).toBe('/');
    const other = await createVault('andere passphrase', 100_000);
    req.flush({
      entries: [
        {
          name: await sealName(made.vault, 'zettel.txt'),
          dir: false,
          size: 100,
          modified: '2026-11-18T10:00:00Z',
        },
        {
          name: await sealName(made.vault, 'Arzt'),
          dir: true,
          size: 0,
          modified: '2026-11-18T10:00:00Z',
        },
        {
          name: await sealName(other.vault, 'fremd'),
          dir: false,
          size: 1,
          modified: '2026-11-18T10:00:00Z',
        },
      ],
    });
    await shown('zettel.txt');
    const rows = page().querySelectorAll('tbody tr');
    expect(rows.length).toBe(2);
    expect(rows[0].textContent).toContain('📁 Arzt');
    expect(page().textContent).toContain('1 Einträge lassen sich');
  });

  it('opens with the recovery key and asks for a new passphrase', async () => {
    await start(made.keys);
    [...page().querySelectorAll('button')]
      .find((b) => b.textContent?.includes('vergessen'))!
      .click();
    fixture.detectChanges();
    field('recovery').value = made.recovery.toLowerCase();
    submit();
    (await request('/api/vault/files')).flush({ entries: [] });
    await shown('bitte unten eine neue Passphrase setzen');

    field('current').value = made.recovery;
    page().querySelector<HTMLInputElement>('.settings input[name=passphrase]')!.value =
      'ganz neue passphrase';
    page().querySelector<HTMLInputElement>('.settings input[name=repeat]')!.value =
      'ganz neue passphrase';
    submit('.settings form');
    const req = await request('/api/vault/keys');
    const body = req.request.body as { keys: VaultKeys; proof: string };
    expect(body.proof).toBe(made.proof);
    expect(body.keys.recovery).toEqual(made.keys.recovery);
    req.flush({ keys: body.keys });
    await shown('Neue Passphrase gespeichert');
  });

  it('encrypts uploads and decrypts downloads in the browser', async () => {
    await unlocked();
    (await request('/api/vault/files')).flush({ entries: [] });
    await shown('leer');

    const input = page().querySelector<HTMLInputElement>('input[type=file]')!;
    Object.defineProperty(input, 'files', {
      value: [new File(['Befund: alles gut'], 'befund.txt', { type: 'text/plain' })],
    });
    input.dispatchEvent(new Event('change'));
    const put = await request('/api/vault/file');
    const sent = put.request.body as Blob;
    const text = await sent.text();
    expect(text.startsWith('DDV1')).toBe(true);
    expect(text).not.toContain('Befund');
    expect(put.request.params.get('path')).not.toContain('befund');
    expect(await (await openFile(made.vault, sent)).text()).toBe('Befund: alles gut');
    put.flush({ size: sent.size });
    const sealedName = put.request.params.get('path')!.slice(1);
    (await request('/api/vault/files')).flush({
      entries: [
        { name: sealedName, dir: false, size: sent.size, modified: '2026-11-18T10:00:00Z' },
      ],
    });
    await shown('verschlüsselt hochgeladen');

    const urls: Blob[] = [];
    URL.createObjectURL = (blob: Blob) => {
      urls.push(blob);
      return 'blob:x';
    };
    URL.revokeObjectURL = () => undefined;
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    [...page().querySelectorAll('button')]
      .find((b) => b.textContent?.includes('Herunterladen'))!
      .click();
    (await request('/api/vault/file')).flush(sent);
    await shown('„befund.txt“ entschlüsselt');
    expect(await urls[0].text()).toBe('Befund: alles gut');
  });

  it('shows when a download does not decrypt', async () => {
    await unlocked();
    const name = await sealName(made.vault, 'kaputt.pdf');
    (await request('/api/vault/files')).flush({
      entries: [{ name, dir: false, size: 99, modified: '2026-11-18T10:00:00Z' }],
    });
    await shown('kaputt.pdf');
    [...page().querySelectorAll('button')]
      .find((b) => b.textContent?.includes('Herunterladen'))!
      .click();
    const other = await createVault('andere passphrase', 100_000);
    (await request('/api/vault/file')).flush(await sealFile(other.vault, new Blob(['x'])));
    await shown('beschädigt');
  });

  it('locks', async () => {
    await unlocked();
    (await request('/api/vault/files')).flush({ entries: [] });
    await shown('leer');
    [...page().querySelectorAll('button')]
      .find((b) => b.textContent?.trim() === 'Sperren')!
      .click();
    fixture.detectChanges();
    expect(page().textContent).toContain('Tresor gesperrt');
    expect(TestBed.inject(VaultService).vault()).toBeNull();
  });
});
