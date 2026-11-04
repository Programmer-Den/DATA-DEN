import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { Grant } from '../shares/grant';
import { TeamsView } from './teams';

const anna: Grant = {
  id: 'a'.repeat(32),
  owner: '@familie',
  path: '/',
  name: 'familie',
  to: 'anna',
  write: true,
  team: true,
  created: '2026-11-01T10:00:00Z',
};

function button(el: Element, text: string): HTMLButtonElement {
  return [...el.querySelectorAll('button')].find((b) => b.textContent?.includes(text))!;
}

describe('TeamsView', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<TeamsView>;

  beforeEach(async () => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    fixture = TestBed.createComponent(TeamsView);
    http.expectOne('/api/teams').flush({ teams: [{ name: 'familie', members: [anna] }] });
    http.expectOne('/api/users').flush({
      users: [
        { name: 'chef', admin: true, owner: true, quota: 0, used: 0, twoFactor: false, tokens: 0 },
        {
          name: 'anna',
          admin: false,
          owner: false,
          quota: 0,
          used: 0,
          twoFactor: false,
          tokens: 0,
        },
        { name: 'bob', admin: false, owner: false, quota: 0, used: 0, twoFactor: false, tokens: 0 },
      ],
    });
    await fixture.whenStable();
  });

  afterEach(() => {
    http.verify();
    vi.restoreAllMocks();
  });

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  function team(): HTMLElement {
    return page().querySelector('section.team')!;
  }

  it('lists the team folders with their members', () => {
    expect(team().textContent).toContain('familie');
    expect(team().textContent).toContain('anna: Lesen und Schreiben');
    const options = [...team().querySelectorAll('option')].map((o) => o.value);
    expect(options).toEqual(['chef', 'anna', 'bob']);
  });

  it('creates a team folder', async () => {
    const input = page().querySelector<HTMLInputElement>('input[name=name]')!;
    input.value = 'arbeit';
    page().querySelector('form')!.dispatchEvent(new Event('submit'));
    const req = http.expectOne('/api/teams');
    expect(req.request.method).toBe('POST');
    expect(req.request.body).toEqual({ name: 'arbeit' });
    req.flush({ name: 'arbeit', members: [] });
    http.expectOne('/api/teams').flush({ teams: [] });
    await fixture.whenStable();
    expect(page().querySelector('[role=status]')?.textContent).toContain('„arbeit“ angelegt');
    expect(input.value).toBe('');
  });

  it('adds a member who only reads', async () => {
    team().querySelector<HTMLSelectElement>('select')!.value = 'bob';
    team().querySelector('form')!.dispatchEvent(new Event('submit'));
    const req = http.expectOne('/api/teams/familie/members');
    expect(req.request.body).toEqual({ to: 'bob', write: false });
    req.flush({ ...anna, to: 'bob', write: false });
    http.expectOne('/api/teams').flush({ teams: [] });
    await fixture.whenStable();
    expect(page().querySelector('[role=status]')?.textContent).toContain('bob in „familie“');
  });

  it('removes a member', async () => {
    button(team(), 'Entfernen').click();
    const req = http.expectOne('/api/teams/familie/members/delete');
    expect(req.request.body).toEqual({ to: 'anna' });
    req.flush({ to: 'anna' });
    http.expectOne('/api/teams').flush({ teams: [] });
    await fixture.whenStable();
    expect(page().textContent).toContain('Keine Team-Ordner');
  });

  it('deletes a team folder only after asking', async () => {
    vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    button(team(), 'Löschen').click();
    http.expectNone('/api/teams/familie/delete');
    button(team(), 'Löschen').click();
    http
      .expectOne('/api/teams/familie/delete')
      .flush({ error: 'Kein Zugriff' }, { status: 403, statusText: 'Forbidden' });
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toContain('Kein Zugriff');
  });
});
