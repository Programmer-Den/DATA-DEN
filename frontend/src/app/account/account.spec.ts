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

  async function start(owner: boolean, quota: number): Promise<ComponentFixture<AccountView>> {
    TestBed.inject(AuthService).owner.set(owner);
    const fixture = TestBed.createComponent(AccountView);
    http.expectOne('/api/usage').flush({ used: 512 * 1024 ** 2, quota });
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
});
