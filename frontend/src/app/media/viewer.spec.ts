import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { Item } from './item';
import { MAX_TEXT, Viewer } from './viewer';

const modified = '2026-10-25T10:00:00Z';
const items: Item[] = [
  { path: '/Fotos/a.jpg', name: 'a.jpg', size: 10, modified, kind: 'photo', preview: true },
  { path: '/film.mp4', name: 'film.mp4', size: 10, modified, kind: 'video', preview: true },
  { path: '/brief.pdf', name: 'brief.pdf', size: 10, modified, kind: 'document', preview: true },
  { path: '/notiz.txt', name: 'notiz.txt', size: 5, modified, kind: 'document', preview: true },
  { path: '/tabelle.xlsx', name: 'tabelle.xlsx', size: 10, modified, kind: 'document' },
  {
    path: '/gross.log',
    name: 'gross.log',
    size: MAX_TEXT + 1,
    modified,
    kind: 'document',
    preview: true,
  },
];

describe('Viewer', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<Viewer>;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    fixture = TestBed.createComponent(Viewer);
    fixture.componentRef.setInput('items', items);
  });

  afterEach(() => http.verify());

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  async function show(index: number): Promise<void> {
    fixture.componentRef.setInput('index', index);
    await fixture.whenStable();
  }

  it('shows a photo', async () => {
    await show(0);
    expect(page().querySelector('img')?.getAttribute('src')).toBe(
      '/api/view?path=%2FFotos%2Fa.jpg',
    );
    expect(page().querySelector('a[download]')?.getAttribute('href')).toBe(
      '/api/download?path=%2FFotos%2Fa.jpg',
    );
    expect(page().textContent).toContain('1 / 6');
  });

  it('plays a video', async () => {
    await show(1);
    const video = page().querySelector('video')!;
    expect(video.getAttribute('src')).toBe('/api/view?path=%2Ffilm.mp4');
    expect(video.hasAttribute('controls')).toBe(true);
  });

  it('says when the browser cannot show a file', async () => {
    await show(1);
    page().querySelector('video')!.dispatchEvent(new Event('error'));
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toContain('nicht anzeigen');
  });

  it('shows a PDF in a frame', async () => {
    await show(2);
    expect(page().querySelector('iframe')?.getAttribute('src')).toBe('/api/view?path=%2Fbrief.pdf');
    expect(page().textContent).toContain('In neuem Tab öffnen');
  });

  it('shows a text file', async () => {
    await show(3);
    http.expectOne('/api/view?path=%2Fnotiz.txt').flush('Hallo');
    await fixture.whenStable();
    expect(page().querySelector('pre')?.textContent).toBe('Hallo');
  });

  it('offers only the download without a preview or for large text', async () => {
    for (const index of [4, 5]) {
      await show(index);
      expect(page().textContent).toContain('Keine Vorschau möglich');
      expect(page().querySelector('a[download]')).not.toBeNull();
    }
  });

  it('goes through the list with the buttons and keys, and closes with Escape', async () => {
    await show(0);
    const changes: number[] = [];
    fixture.componentInstance.index.subscribe((i) => changes.push(i));
    let closed = false;
    fixture.componentInstance.closed.subscribe(() => (closed = true));

    expect(
      page().querySelector<HTMLButtonElement>('[aria-label="Vorherige Datei"]')!.disabled,
    ).toBe(true);
    page().querySelector<HTMLButtonElement>('[aria-label="Nächste Datei"]')!.click();
    await fixture.whenStable();
    expect(page().querySelector('video')).not.toBeNull();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft' }));
    await fixture.whenStable();
    expect(page().querySelector('img')).not.toBeNull();
    expect(changes).toEqual([1, 0]);

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(closed).toBe(true);
  });
});
