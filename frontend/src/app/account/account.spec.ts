import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { AuthService } from '../auth.service';
import { AccountView } from './account';

describe('AccountView', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => http.verify());

  async function start(
    owner: boolean,
    quota: number,
    enabled = false,
  ): Promise<ComponentFixture<AccountView>> {
    TestBed.inject(AuthService).owner.set(owner);
    const fixture = TestBed.createComponent(AccountView);
    http.expectOne('/api/usage').flush({ used: 512 * 1024 ** 2, quota });
    http.expectOne('/api/account/2fa').flush({ enabled, recoveryLeft: enabled ? 9 : 0 });
    await fixture.whenStable();
    return fixture;
  }

  function page(fixture: ComponentFixture<AccountView>): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  function fill(fixture: ComponentFixture<AccountView>, values: Record<string, string>): void {
    for (const [name, value] of Object.entries(values)) {
      page(fixture).querySelector<HTMLInputElement>(`input[name=${name}]`)!.value = value;
    }
    page(fixture).querySelector('form')!.dispatchEvent(new Event('submit'));
  }

  function button(fixture: ComponentFixture<AccountView>, text: string): HTMLButtonElement {
    return [...page(fixture).querySelectorAll('button')].find((b) =>
      b.textContent?.includes(text),
    )!;
  }

  it('shows the storage used of the quota', async () => {
    const fixture = await start(false, 1024 ** 3);
    expect(page(fixture).textContent).toContain('512 MB von 1 GB belegt');
    expect(page(fixture).querySelector('progress')?.value).toBe(50);
  });

  it('shows the storage used without a quota', async () => {
    const fixture = await start(true, 0);
    expect(page(fixture).textContent).toContain('512 MB belegt, ohne Grenze');
    expect(page(fixture).querySelector('form')).toBeNull();
    expect(page(fixture).textContent).toContain('DATA_DEN_PASSWORD_HASH');
    expect(page(fixture).textContent).toContain('Zwei-Faktor-Anmeldung');
  });

  it('changes the password', async () => {
    const fixture = await start(false, 0);
    fill(fixture, {
      current: 'alt-passwort',
      password: 'neues-passwort',
      repeat: 'neues-passwort',
    });
    const req = http.expectOne('/api/account/password');
    expect(req.request.body).toEqual({ current: 'alt-passwort', password: 'neues-passwort' });
    req.flush({});
    await fixture.whenStable();
    expect(page(fixture).querySelector('[role=status]')?.textContent).toContain(
      'Passwort geändert',
    );
  });

  it('refuses two different new passwords', async () => {
    const fixture = await start(false, 0);
    fill(fixture, {
      current: 'alt-passwort',
      password: 'neues-passwort',
      repeat: 'anderes-passwort',
    });
    await fixture.whenStable();
    expect(page(fixture).querySelector('[role=alert]')?.textContent).toContain('verschieden');
  });

  it('sets up the two-factor login and shows the recovery codes once', async () => {
    const fixture = await start(true, 0);
    button(fixture, 'Einrichten').click();
    http.expectOne('/api/account/2fa/setup').flush({
      secret: 'ABCDEF',
      uri: 'otpauth://totp/DATA-DEN:chriszly?secret=ABCDEF',
      qr: 'data:image/png;base64,iVBOR',
    });
    await fixture.whenStable();
    expect(page(fixture).querySelector('img')?.getAttribute('src')).toBe(
      'data:image/png;base64,iVBOR',
    );
    expect(page(fixture).querySelector('a')?.getAttribute('href')).toContain('otpauth://totp/');
    expect(page(fixture).textContent).toContain('ABCDEF');

    page(fixture).querySelector<HTMLInputElement>('form.code input')!.value = ' 123456 ';
    page(fixture).querySelector('form.code')!.dispatchEvent(new Event('submit'));
    const req = http.expectOne('/api/account/2fa/enable');
    expect(req.request.body).toEqual({ code: '123456' });
    req.flush({ recovery: ['aaaaa-bbbbb', 'ccccc-ddddd'] });
    http.expectOne('/api/account/2fa').flush({ enabled: true, recoveryLeft: 2 });
    await fixture.whenStable();
    expect(page(fixture).textContent).toContain('aaaaa-bbbbb');
    expect(page(fixture).textContent).toContain('Noch 2 Wiederherstellungscodes');
    expect(TestBed.inject(AuthService).twoFactor()).toBe(true);
  });

  it('turns the two-factor login off with password and code', async () => {
    const fixture = await start(true, 0, true);
    const form = page(fixture).querySelector('form')!;
    form.querySelector<HTMLInputElement>('[name=password]')!.value = 'richtig';
    form.querySelector<HTMLInputElement>('[name=code]')!.value = '654321';
    form.dispatchEvent(new Event('submit'));
    const req = http.expectOne('/api/account/2fa/disable');
    expect(req.request.body).toEqual({ password: 'richtig', code: '654321' });
    req.flush({});
    http.expectOne('/api/account/2fa').flush({ enabled: false, recoveryLeft: 0 });
    await fixture.whenStable();
    expect(page(fixture).querySelector('[role=status]')?.textContent).toContain('ist aus');
  });
});
