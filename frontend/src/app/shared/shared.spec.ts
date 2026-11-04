import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { Grant } from '../shares/grant';
import { GrantForm } from '../shares/grant-form';
import { Shared } from './shared';

const reading: Grant = {
  id: 'a'.repeat(32),
  owner: 'chef',
  path: '/Projekt',
  name: 'Projekt',
  to: 'anna',
  write: false,
  created: '2026-11-01T10:00:00Z',
};
const writing: Grant = { ...reading, id: 'b'.repeat(32), name: 'Fotos', write: true };
const entries = [
  { name: '2026', dir: true, size: 0, modified: '2026-11-01T10:00:00Z' },
  { name: 'a b.txt', dir: false, size: 3, modified: '2026-11-01T10:00:00Z' },
];

function setup(): HttpTestingController {
  TestBed.configureTestingModule({
    providers: [provideHttpClient(), provideHttpClientTesting()],
  });
  return TestBed.inject(HttpTestingController);
}

function button(el: HTMLElement, text: string): HTMLButtonElement {
  return [...el.querySelectorAll('button')].find((b) => b.textContent?.includes(text))!;
}

describe('Shared', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<Shared>;

  beforeEach(async () => {
    http = setup();
    fixture = TestBed.createComponent(Shared);
    http.expectOne('/api/grants').flush({
      mine: [],
      withMe: [
        reading,
        writing,
        { ...reading, id: 'c'.repeat(32), name: 'Alt', missing: true },
        { ...writing, id: 'd'.repeat(32), owner: '@familie', name: 'familie', team: true },
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

  it('lists the folders shared with me', () => {
    const rows = page().querySelectorAll('tbody tr');
    expect(rows.length).toBe(4);
    expect(rows[0].textContent).toContain('Projekt');
    expect(rows[0].textContent).toContain('chef');
    expect(rows[0].textContent).toContain('Nur lesen');
    expect(rows[1].textContent).toContain('Lesen und Schreiben');
    expect(rows[2].textContent).toContain('nicht mehr vorhanden');
    expect(rows[2].querySelector('button.link')).toBeNull();
    expect(rows[3].textContent).toContain('Team-Ordner');
    expect(rows[3].textContent).not.toContain('@familie');
  });

  it('opens a folder to read, with nothing to change', async () => {
    button(page(), 'Projekt').click();
    await fixture.whenStable();
    const req = http.expectOne(`/api/files?path=/&grant=${reading.id}`);
    req.flush({ path: '/', entries });
    await fixture.whenStable();
    expect(page().querySelector('nav.crumbs')?.textContent).toContain('Projekt');
    const download = [...page().querySelectorAll<HTMLAnchorElement>('a[download]')][0];
    expect(download.getAttribute('href')).toBe(
      `/api/download?path=%2Fa%20b.txt&grant=${reading.id}`,
    );
    expect(page().querySelector('input[type=file]')).toBeNull();
    expect(button(page(), 'Neuer Ordner')).toBeUndefined();
    expect(page().querySelector('[title="Umbenennen"]')).toBeNull();
    expect(page().querySelector('[title="Freigeben"]')).toBeNull();
    expect(page().querySelector('[title="Mit einem Konto teilen"]')).toBeNull();

    button(page(), '2026').click();
    http
      .expectOne(`/api/files?path=/2026&grant=${reading.id}`)
      .flush({ path: '/2026', entries: [] });
    await fixture.whenStable();
    button(page(), 'Geteilt mit mir').click();
    await fixture.whenStable();
    expect(page().querySelectorAll('tbody tr').length).toBe(4);
  });

  it('changes a folder it may write to within it', async () => {
    button(page(), 'Fotos').click();
    await fixture.whenStable();
    http.expectOne(`/api/files?path=/&grant=${writing.id}`).flush({ path: '/', entries });
    await fixture.whenStable();
    expect(page().querySelector('input[type=file]')).not.toBeNull();
    expect(page().querySelector('[title="Freigeben"]')).toBeNull();
    (page().querySelector('[title="In den Papierkorb"]') as HTMLButtonElement).click();
    const req = http.expectOne(`/api/delete?grant=${writing.id}`);
    expect(req.request.body).toEqual({ path: '/2026' });
    req.flush({ path: '/2026' });
    http.expectOne(`/api/files?path=/&grant=${writing.id}`).flush({ path: '/', entries: [] });
    await fixture.whenStable();
    // Only the owner can restore it, so there is nothing to undo.
    expect(button(page(), 'Rückgängig')).toBeUndefined();
  });

  it('leaves a folder after asking', async () => {
    vi.spyOn(window, 'confirm').mockReturnValueOnce(true);
    button(page().querySelectorAll('tbody tr')[0] as HTMLElement, 'Verlassen').click();
    const req = http.expectOne('/api/grants/delete');
    expect(req.request.body).toEqual({ id: reading.id });
    req.flush({ id: reading.id });
    await fixture.whenStable();
    http.expectOne('/api/grants').flush({ mine: [], withMe: [] });
    await fixture.whenStable();
    expect(page().textContent).toContain('Niemand teilt');
  });
});

describe('GrantForm', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<GrantForm>;

  beforeEach(async () => {
    http = setup();
    fixture = TestBed.createComponent(GrantForm);
    fixture.componentRef.setInput('path', '/Projekt');
    fixture.componentRef.setInput('name', 'Projekt');
    fixture.detectChanges();
    http.expectOne('/api/accounts').flush({ names: ['anna', 'bob'] });
    await fixture.whenStable();
  });

  afterEach(() => http.verify());

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  it('shares a folder with an account to change it', async () => {
    page().querySelector<HTMLSelectElement>('select')!.value = 'bob';
    page().querySelector<HTMLInputElement>('input[type=checkbox]')!.checked = true;
    page().querySelector('form')!.dispatchEvent(new Event('submit'));
    const req = http.expectOne('/api/grants');
    expect(req.request.body).toEqual({ path: '/Projekt', to: 'bob', write: true });
    req.flush({ ...writing, to: 'bob', name: 'Projekt' });
    await fixture.whenStable();
    expect(page().textContent).toContain('bob sieht „Projekt“');
    expect(page().textContent).toContain('darf ändern');
  });

  it('shows why sharing failed', async () => {
    page().querySelector('form')!.dispatchEvent(new Event('submit'));
    const req = http.expectOne('/api/grants');
    expect(req.request.body).toEqual({ path: '/Projekt', to: 'anna', write: false });
    req.flush({ error: 'Unbekanntes Konto' }, { status: 400, statusText: 'Bad Request' });
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toContain('Unbekanntes Konto');
  });
});
