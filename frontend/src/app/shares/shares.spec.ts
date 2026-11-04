import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { Grant } from './grant';
import { Share } from './share';
import { ShareForm } from './share-form';
import { SharesView } from './shares';

const id = '0123456789abcdef0123456789abcdef';
const folder: Share = {
  id,
  path: '/Fotos/2026',
  name: '2026',
  dir: true,
  created: '2026-11-01T10:00:00Z',
  expires: '2026-11-08T10:00:00Z',
  password: true,
};
const gone: Share = {
  id: 'f'.repeat(32),
  path: '/alt.txt',
  name: 'alt.txt',
  dir: false,
  created: '2026-10-01T10:00:00Z',
  expires: '2026-12-01T10:00:00Z',
  password: false,
  missing: true,
};

const projekt: Grant = {
  id: 'a'.repeat(32),
  owner: 'chef',
  path: '/Projekt',
  name: 'Projekt',
  to: 'anna',
  write: true,
  created: '2026-11-01T10:00:00Z',
};

function setup(): HttpTestingController {
  TestBed.configureTestingModule({
    providers: [provideHttpClient(), provideHttpClientTesting()],
  });
  return TestBed.inject(HttpTestingController);
}

function button(el: HTMLElement, text: string): HTMLButtonElement {
  return [...el.querySelectorAll('button')].find((b) => b.textContent?.includes(text))!;
}

describe('SharesView', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<SharesView>;

  beforeEach(async () => {
    http = setup();
    fixture = TestBed.createComponent(SharesView);
    http.expectOne('/api/shares').flush({ maxDays: 365, items: [folder, gone] });
    http.expectOne('/api/grants').flush({ mine: [projekt], withMe: [] });
    await fixture.whenStable();
  });

  afterEach(() => {
    http.verify();
    vi.restoreAllMocks();
  });

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  /** The rows of the links; the shared folders follow in a second table. */
  function linkRows(): NodeListOf<HTMLElement> {
    return page().querySelectorAll('table')[0].querySelectorAll('tbody tr');
  }

  it('lists the links, until when they work and whether they need a password', () => {
    const rows = linkRows();
    expect(rows.length).toBe(2);
    expect(rows[0].textContent).toContain('📁 2026');
    expect(rows[0].textContent).toContain('/Fotos/2026');
    expect(rows[0].textContent).toContain('08.11.2026');
    expect(rows[0].textContent).toContain('ja');
    expect(rows[1].textContent).toContain('nicht mehr vorhanden');
    expect(rows[1].textContent).toContain('nein');
  });

  it('copies a link', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    button(linkRows()[0] as HTMLElement, 'kopieren').click();
    await fixture.whenStable();
    expect(writeText).toHaveBeenCalledWith(`${location.origin}/s/${id}`);
    expect(page().querySelector('[role=status]')?.textContent).toContain('kopiert');
  });

  it('revokes only after asking', async () => {
    vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    const row = linkRows()[0] as HTMLElement;
    button(row, 'Widerrufen').click();
    http.expectNone('/api/shares/delete');
    button(row, 'Widerrufen').click();
    const req = http.expectOne('/api/shares/delete');
    expect(req.request.body).toEqual({ id });
    req.flush({ id });
    await fixture.whenStable();
    http.expectOne('/api/shares').flush({ maxDays: 365, items: [] });
    await fixture.whenStable();
    expect(page().textContent).toContain('Keine Freigaben');
  });

  it('lists the folders shared with other accounts and ends one', async () => {
    const row = page().querySelectorAll('table')[1].querySelector('tbody tr') as HTMLElement;
    expect(row.textContent).toContain('Projekt');
    expect(row.textContent).toContain('anna');
    expect(row.textContent).toContain('Lesen und Schreiben');
    vi.spyOn(window, 'confirm').mockReturnValueOnce(true);
    button(row, 'Beenden').click();
    const req = http.expectOne('/api/grants/delete');
    expect(req.request.body).toEqual({ id: projekt.id });
    req.flush({ id: projekt.id });
    await fixture.whenStable();
    http.expectOne('/api/grants').flush({ mine: [], withMe: [] });
    await fixture.whenStable();
    expect(page().textContent).toContain('Keine geteilten Ordner');
  });
});

describe('ShareForm', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<ShareForm>;

  beforeEach(async () => {
    http = setup();
    fixture = TestBed.createComponent(ShareForm);
    fixture.componentRef.setInput('path', '/Fotos/2026');
    fixture.componentRef.setInput('name', '2026');
    await fixture.whenStable();
  });

  afterEach(() => http.verify());

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  it('makes a link for 7 days without a password by default', async () => {
    page().querySelector('form')!.dispatchEvent(new Event('submit'));
    const req = http.expectOne('/api/shares');
    expect(req.request.body).toEqual({
      path: '/Fotos/2026',
      days: 7,
      password: '',
      upload: false,
    });
    req.flush({ ...folder, password: false });
    await fixture.whenStable();
    expect(page().querySelector<HTMLInputElement>('input[readonly]')!.value).toBe(
      `${location.origin}/s/${id}`,
    );
  });

  it('sends the chosen time and password', async () => {
    page().querySelector<HTMLSelectElement>('select')!.value = '30';
    page().querySelector<HTMLInputElement>('input[type=password]')!.value = 'geheim';
    page().querySelector('form')!.dispatchEvent(new Event('submit'));
    const req = http.expectOne('/api/shares');
    expect(req.request.body).toEqual({
      path: '/Fotos/2026',
      days: 30,
      password: 'geheim',
      upload: false,
    });
    req.flush({ error: 'Nicht gefunden' }, { status: 404, statusText: 'Not Found' });
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toContain('Nicht gefunden');
  });

  it('offers an upload link only for a folder', async () => {
    expect(page().querySelector('input[type=checkbox]')).toBeNull();
    fixture.componentRef.setInput('dir', true);
    await fixture.whenStable();
    const upload = page().querySelector<HTMLInputElement>('input[type=checkbox]')!;
    upload.checked = true;
    upload.dispatchEvent(new Event('change'));
    page().querySelector('form')!.dispatchEvent(new Event('submit'));
    const req = http.expectOne('/api/shares');
    expect(req.request.body).toEqual({ path: '/Fotos/2026', days: 7, password: '', upload: true });
    req.flush({ ...folder, upload: true });
    await fixture.whenStable();
    expect(page().textContent).toContain('Dateien in den Ordner legen');
  });

  it('closes', () => {
    let closed = false;
    fixture.componentInstance.closed.subscribe(() => (closed = true));
    button(page(), 'Abbrechen').click();
    expect(closed).toBe(true);
  });
});
