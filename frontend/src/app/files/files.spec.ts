import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { Entry, Files, formatSize } from './files';

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

describe('formatSize', () => {
  it('picks a unit', () => {
    expect(formatSize(0)).toBe('0 B');
    expect(formatSize(1023)).toBe('1.023 B');
    expect(formatSize(1572864)).toBe('1,5 MB');
    expect(formatSize(4 * 1024 ** 4)).toBe('4 TB');
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
