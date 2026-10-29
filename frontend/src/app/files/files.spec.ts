import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { Entry, Files } from './files';

const photos: Entry = { name: 'Fotos', dir: true, size: 0, modified: '2026-10-25T10:00:00Z' };
const notes: Entry = {
  name: 'notizen 1.txt',
  dir: false,
  size: 1536,
  modified: '2026-10-24T08:30:00Z',
};

describe('Files', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<Files>;

  beforeEach(async () => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    fixture = TestBed.createComponent(Files);
    http.expectOne('/api/files?path=/').flush({ path: '/', entries: [photos, notes] });
    await fixture.whenStable();
  });

  afterEach(() => http.verify());

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  it('lists the top folder', () => {
    const rows = page().querySelectorAll('tbody tr');
    expect(rows.length).toBe(2);
    expect(rows[0].textContent).toContain('Fotos');
    expect(rows[1].textContent).toContain('1,5 KB');
  });

  it('links files to their download', () => {
    const link = page().querySelector<HTMLAnchorElement>('tbody a')!;
    expect(link.getAttribute('href')).toBe('/api/download?path=%2Fnotizen%201.txt');
    expect(link.hasAttribute('download')).toBe(true);
  });

  it('opens a file with a preview in the viewer', async () => {
    page().querySelector<HTMLButtonElement>('tbody button')!.click();
    http.expectOne('/api/files?path=/Fotos').flush({
      path: '/Fotos',
      entries: [
        {
          name: 'a.jpg',
          dir: false,
          size: 1,
          modified: notes.modified,
          kind: 'photo',
          preview: true,
        },
        {
          name: 'b.jpg',
          dir: false,
          size: 1,
          modified: notes.modified,
          kind: 'photo',
          preview: true,
        },
      ],
    });
    await fixture.whenStable();
    page().querySelectorAll<HTMLButtonElement>('tbody td:first-child button')[1].click();
    await fixture.whenStable();
    expect(page().querySelector('app-viewer')?.textContent).toContain('2 / 2');
    expect(page().querySelector('app-viewer img')?.getAttribute('src')).toBe(
      '/api/view?path=%2FFotos%2Fb.jpg',
    );
  });

  it('opens a folder and goes back through the path bar', async () => {
    page().querySelector<HTMLButtonElement>('tbody button')!.click();
    http.expectOne('/api/files?path=/Fotos').flush({ path: '/Fotos', entries: [] });
    await fixture.whenStable();
    expect(page().textContent).toContain('Dieser Ordner ist leer');
    expect(page().querySelector('[aria-current=page]')?.textContent).toBe('Fotos');

    page().querySelector<HTMLButtonElement>('nav button')!.click();
    http.expectOne('/api/files?path=/').flush({ path: '/', entries: [photos] });
    await fixture.whenStable();
    expect(page().querySelector('[aria-current=page]')?.textContent).toBe('Start');
  });

  it("shows the server's message when a folder cannot be opened", async () => {
    page().querySelector<HTMLButtonElement>('tbody button')!.click();
    http
      .expectOne('/api/files?path=/Fotos')
      .flush({ error: 'Nicht gefunden' }, { status: 404, statusText: 'Not Found' });
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toContain('Nicht gefunden');
  });
});

describe('Files management', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<Files>;

  beforeEach(async () => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    fixture = TestBed.createComponent(Files);
    http.expectOne('/api/files?path=/').flush({ path: '/', entries: [photos, notes] });
    await fixture.whenStable();
  });

  afterEach(() => {
    http.verify();
    vi.restoreAllMocks();
  });

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  function button(label: string): HTMLButtonElement {
    return page().querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)!;
  }

  async function submit(input: HTMLInputElement, value: string): Promise<void> {
    input.value = value;
    input.form!.dispatchEvent(new Event('submit'));
    await fixture.whenStable();
  }

  async function reload(path: string): Promise<void> {
    http.expectOne(`/api/files?path=${path}`).flush({ path, entries: [photos, notes] });
    await fixture.whenStable();
  }

  it('creates a folder', async () => {
    [...page().querySelectorAll('button')]
      .find((b) => b.textContent?.includes('Neuer Ordner'))!
      .click();
    await fixture.whenStable();
    await submit(page().querySelector('input[aria-label="Name des neuen Ordners"]')!, ' Urlaub ');
    const req = http.expectOne('/api/folders');
    expect(req.request.body).toEqual({ path: '/Urlaub' });
    req.flush({ path: '/Urlaub' }, { status: 201, statusText: 'Created' });
    await fixture.whenStable();
    await reload('/');
    expect(page().querySelector('input[aria-label="Name des neuen Ordners"]')).toBeNull();
  });

  it('renames a file', async () => {
    button('notizen 1.txt umbenennen').click();
    await fixture.whenStable();
    const input = page().querySelector<HTMLInputElement>(
      'input[aria-label="Neuer Name für notizen 1.txt"]',
    )!;
    expect(input.value).toBe('notizen 1.txt');
    await submit(input, 'notizen 2.txt');
    const req = http.expectOne('/api/move');
    expect(req.request.body).toEqual({ from: '/notizen 1.txt', to: '/notizen 2.txt' });
    req.flush({ path: '/notizen 2.txt' });
    await fixture.whenStable();
    await reload('/');
  });

  it('shows why a rename failed and keeps the form', async () => {
    button('notizen 1.txt umbenennen').click();
    await fixture.whenStable();
    await submit(page().querySelector('form.inline input')!, 'Fotos');
    http
      .expectOne('/api/move')
      .flush({ error: 'Existiert bereits' }, { status: 409, statusText: 'Conflict' });
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toContain('Existiert bereits');
    expect(page().querySelector('form.inline')).not.toBeNull();
  });

  it('moves a file into another folder', async () => {
    button('notizen 1.txt verschieben').click();
    await fixture.whenStable();
    expect(page().querySelector('.moving')?.textContent).toContain('notizen 1.txt');

    page().querySelector<HTMLButtonElement>('tbody td:first-child button')!.click();
    http.expectOne('/api/files?path=/Fotos').flush({ path: '/Fotos', entries: [] });
    await fixture.whenStable();
    [...page().querySelectorAll('.moving button')]
      .find((b) => b.textContent?.includes('Hierher'))!
      .dispatchEvent(new Event('click'));
    const req = http.expectOne('/api/move');
    expect(req.request.body).toEqual({ from: '/notizen 1.txt', to: '/Fotos/notizen 1.txt' });
    req.flush({ path: '/Fotos/notizen 1.txt' });
    await fixture.whenStable();
    await reload('/Fotos');
    expect(page().querySelector('.moving')).toBeNull();
  });

  it('deletes after asking', async () => {
    const confirm = vi
      .spyOn(window, 'confirm')
      .mockReturnValueOnce(false)
      .mockReturnValueOnce(true);
    button('Fotos löschen').click();
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('mit seinem ganzen Inhalt'));
    http.expectNone('/api/delete');

    button('Fotos löschen').click();
    const req = http.expectOne('/api/delete');
    expect(req.request.body).toEqual({ path: '/Fotos' });
    req.flush({ path: '/Fotos' });
    await fixture.whenStable();
    await reload('/');
  });
});

describe('Files upload', () => {
  it('uploads picked files into the open folder and shows them', async () => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    const http = TestBed.inject(HttpTestingController);
    const fixture = TestBed.createComponent(Files);
    http.expectOne('/api/files?path=/').flush({ path: '/', entries: [photos] });
    await fixture.whenStable();

    const input = (fixture.nativeElement as HTMLElement).querySelector<HTMLInputElement>(
      'input[type=file][multiple]',
    )!;
    const picked = [new File([], 'neu.txt')];
    Object.defineProperty(input, 'files', { value: Object.assign(picked, { item: () => null }) });
    input.dispatchEvent(new Event('change'));
    await new Promise((resolve) => setTimeout(resolve));

    const start = http.expectOne('/api/uploads');
    expect(start.request.body).toEqual({ path: '/neu.txt', size: 0 });
    start.flush({ id: 'u1', offset: 0 });
    await new Promise((resolve) => setTimeout(resolve));

    http.expectOne('/api/files?path=/').flush({ path: '/', entries: [photos, notes] });
    await fixture.whenStable();
    expect((fixture.nativeElement as HTMLElement).textContent).toContain('fertig');
    http.verify();
  });
});
