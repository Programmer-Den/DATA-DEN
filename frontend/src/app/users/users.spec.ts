import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { AuthService } from '../auth.service';
import { User, UsersView, parseQuota, quotaField } from './users';

const users: User[] = [
  { name: 'chriszly', admin: true, owner: true, quota: 0, used: 2 * 1024 ** 3, twoFactor: false },
  { name: 'anna', admin: false, owner: false, quota: 50 * 1024 ** 3, used: 1024, twoFactor: true },
];

describe('quota fields', () => {
  it('turns GB into bytes and back', () => {
    expect(parseQuota('')).toBe(0);
    expect(parseQuota('1,5')).toBe(1.5 * 1024 ** 3);
    expect(parseQuota('-1')).toBeNull();
    expect(parseQuota('viel')).toBeNull();
    expect(quotaField(50 * 1024 ** 3)).toBe('50');
    expect(quotaField(0)).toBe('');
  });
});

describe('UsersView', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<UsersView>;

  beforeEach(async () => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    TestBed.inject(AuthService).user.set('chriszly');
    fixture = TestBed.createComponent(UsersView);
    http.expectOne('/api/users').flush({ users });
    await fixture.whenStable();
  });

  afterEach(() => {
    http.verify();
    vi.restoreAllMocks();
  });

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  function row(i: number): HTMLElement {
    return page().querySelectorAll<HTMLElement>('tbody tr')[i];
  }

  function button(el: HTMLElement, text: string): HTMLButtonElement {
    return [...el.querySelectorAll('button')].find((b) => b.textContent?.includes(text))!;
  }

  it('lists the accounts', () => {
    expect(row(0).textContent).toContain('(Hauptkonto)');
    expect(row(0).textContent).toContain('2 GB');
    expect(row(0).querySelector('button')).toBeNull();
    expect(row(1).querySelector<HTMLInputElement>('.quota input')!.value).toBe('50');
  });

  it('creates an account', async () => {
    const form = page().querySelector<HTMLFormElement>('form.create')!;
    form.querySelector<HTMLInputElement>('[name=name]')!.value = 'bob';
    form.querySelector<HTMLInputElement>('[name=password]')!.value = 'bob-passwort';
    form.querySelector<HTMLInputElement>('[name=quota]')!.value = '10';
    form.dispatchEvent(new Event('submit'));
    const req = http.expectOne((r) => r.method === 'POST' && r.url === '/api/users');
    expect(req.request.body).toEqual({
      name: 'bob',
      password: 'bob-passwort',
      admin: false,
      quota: 10 * 1024 ** 3,
    });
    req.flush({});
    http.expectOne('/api/users').flush({ users });
    await fixture.whenStable();
    expect(page().querySelector('[role=status]')?.textContent).toContain('„bob“ angelegt');
  });

  it('changes the quota', async () => {
    row(1).querySelector<HTMLInputElement>('.quota input')!.value = '';
    row(1).querySelector('form')!.dispatchEvent(new Event('submit'));
    const req = http.expectOne('/api/users/anna');
    expect(req.request.body).toEqual({ quota: 0 });
    req.flush({});
    http.expectOne('/api/users').flush({ users });
    await fixture.whenStable();
    expect(page().querySelector('[role=status]')?.textContent).toContain('ohne Grenze');
  });

  it('makes an account admin', () => {
    const box = row(1).querySelector<HTMLInputElement>('input[type=checkbox]')!;
    box.checked = true;
    box.dispatchEvent(new Event('change'));
    expect(http.expectOne('/api/users/anna').request.body).toEqual({ admin: true });
  });

  it('sets a password', () => {
    vi.spyOn(window, 'prompt').mockReturnValue('neues-passwort');
    button(row(1), 'Passwort setzen').click();
    expect(http.expectOne('/api/users/anna').request.body).toEqual({ password: 'neues-passwort' });
  });

  it('deletes an account only after asking', async () => {
    const ask = vi.spyOn(window, 'confirm').mockReturnValue(false);
    button(row(1), 'Löschen').click();
    http.expectNone('/api/users/anna/delete');
    ask.mockReturnValue(true);
    button(row(1), 'Löschen').click();
    http.expectOne('/api/users/anna/delete').flush({});
    http.expectOne('/api/users').flush({ users: [users[0]] });
    await fixture.whenStable();
    expect(page().querySelectorAll('tbody tr').length).toBe(1);
  });

  it("shows the server's message", async () => {
    vi.spyOn(window, 'prompt').mockReturnValue('kurz');
    button(row(1), 'Passwort setzen').click();
    http
      .expectOne('/api/users/anna')
      .flush(
        { error: 'Das Passwort braucht mindestens 10 Zeichen' },
        { status: 400, statusText: 'Bad Request' },
      );
    http.expectOne('/api/users').flush({ users });
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toContain('mindestens 10 Zeichen');
  });

  it('turns the second factor off for a lost phone', () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    expect(row(0).textContent).toContain('aus');
    button(row(1), '2FA ausschalten').click();
    expect(http.expectOne('/api/users/anna').request.body).toEqual({ twoFactor: false });
  });
});
