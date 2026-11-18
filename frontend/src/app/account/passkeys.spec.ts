import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { PasskeyInfo, PasskeysView } from './passkeys';

const PHONE: PasskeyInfo = {
  id: 'AQID',
  name: 'Handy',
  created: '2026-11-18T12:00:00Z',
  lastUsed: null,
};

describe('PasskeysView', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    vi.stubGlobal('isSecureContext', true);
    vi.stubGlobal('PublicKeyCredential', class {});
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    http.verify();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  async function start(
    passkeys: PasskeyInfo[],
    available = true,
  ): Promise<ComponentFixture<PasskeysView>> {
    const fixture = TestBed.createComponent(PasskeysView);
    http.expectOne('/api/account/passkeys').flush({ available, passkeys });
    await fixture.whenStable();
    return fixture;
  }

  function page(fixture: ComponentFixture<PasskeysView>): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  it('lists the passkeys', async () => {
    const fixture = await start([PHONE]);
    expect(page(fixture).querySelector('tbody tr')!.textContent).toContain('Handy');
    expect(page(fixture).querySelector('form.new-passkey')).not.toBeNull();
  });

  it('says when the server has no passkeys set up', async () => {
    const fixture = await start([], false);
    expect(page(fixture).textContent).toContain('DATA_DEN_ORIGIN');
    expect(page(fixture).querySelector('form')).toBeNull();
  });

  it('adds a passkey after the password', async () => {
    const fixture = await start([]);
    const create = vi.fn().mockResolvedValue({
      id: 'CQ',
      rawId: new Uint8Array([9]).buffer,
      type: 'public-key',
      authenticatorAttachment: null,
      getClientExtensionResults: () => ({ credProps: { rk: true } }),
      response: {
        clientDataJSON: new Uint8Array([1]).buffer,
        attestationObject: new Uint8Array([2]).buffer,
        getTransports: () => ['internal'],
      },
    });
    vi.stubGlobal('navigator', { ...navigator, credentials: { create } });
    const form = page(fixture).querySelector<HTMLFormElement>('form.new-passkey')!;
    form.querySelector<HTMLInputElement>('input[name=name]')!.value = 'Laptop';
    form.querySelector<HTMLInputElement>('input[name=password]')!.value = 'richtig';
    form.dispatchEvent(new Event('submit'));

    const begin = http.expectOne('/api/account/passkeys/begin');
    expect(begin.request.body).toEqual({ password: 'richtig', code: '' });
    begin.flush({ publicKey: { challenge: 'AQ', user: { id: 'Ag', name: 'chriszly' } } });
    await vi.waitFor(() => expect(create).toHaveBeenCalled());
    const add = await vi.waitFor(() => http.expectOne('/api/account/passkeys'));
    expect(add.request.method).toBe('POST');
    expect(add.request.body).toEqual({
      name: 'Laptop',
      credential: expect.objectContaining({
        rawId: 'CQ',
        response: { clientDataJSON: 'AQ', attestationObject: 'Ag', transports: ['internal'] },
      }),
    });
    add.flush({ ...PHONE, id: 'CQ', name: 'Laptop' }, { status: 201, statusText: 'Created' });
    await vi.waitFor(() =>
      http.expectOne('/api/account/passkeys').flush({ available: true, passkeys: [PHONE] }),
    );
    await fixture.whenStable();
    expect(page(fixture).querySelector('[role=status]')?.textContent).toContain('Laptop');
  });

  it('removes a passkey', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const fixture = await start([PHONE]);
    page(fixture).querySelector<HTMLButtonElement>('tbody button')!.click();
    http.expectOne('/api/account/passkeys/AQID/delete').flush({});
    http.expectOne('/api/account/passkeys').flush({ available: true, passkeys: [] });
    await fixture.whenStable();
    expect(page(fixture).querySelector('table')).toBeNull();
  });
});
