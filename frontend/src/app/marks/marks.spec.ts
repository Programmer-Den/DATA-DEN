import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { Marked, MarksView } from './marks';

const photo: Marked = {
  path: '/Fotos/strand.jpg',
  name: 'strand.jpg',
  dir: false,
  size: 2048,
  modified: '2026-10-01T10:00:00Z',
  kind: 'photo',
  preview: true,
  favourite: true,
  tags: ['Urlaub'],
};
const folder: Marked = {
  path: '/Rechnungen',
  name: 'Rechnungen',
  dir: true,
  size: 0,
  modified: '2026-09-01T10:00:00Z',
  favourite: true,
};
const zip: Marked = {
  path: '/Archiv/alt.zip',
  name: 'alt.zip',
  dir: false,
  size: 4096,
  modified: '2026-08-01T10:00:00Z',
  used: '2026-11-04T20:00:00Z',
};

describe('MarksView', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<MarksView>;

  async function start(tag = ''): Promise<void> {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    fixture = TestBed.createComponent(MarksView);
    fixture.componentRef.setInput('tag', tag);
    await fixture.whenStable();
    http.expectOne('/api/marks').flush({
      tags: [{ name: 'Urlaub', count: 1 }],
      favourites: [folder, photo],
      recent: [{ ...photo, used: '2026-11-04T21:00:00Z' }, zip],
    });
    await fixture.whenStable();
  }

  afterEach(() => http.verify());

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  it('lists the tags, favourites and recently used files', async () => {
    await start();
    const headings = [...page().querySelectorAll('h2')].map((h) => h.textContent);
    expect(headings).toEqual(['Tags', 'Favoriten', 'Zuletzt verwendet']);
    expect(page().querySelector('.tags .tag')?.textContent).toContain('Urlaub');
    const lists = page().querySelectorAll('ul.items');
    expect(lists[0].querySelectorAll('li').length).toBe(2);
    expect(lists[1].textContent).toContain('alt.zip');
    expect(lists[1].querySelector('a')?.getAttribute('href')).toBe(
      '/api/download?path=%2FArchiv%2Falt.zip',
    );
  });

  it('opens a folder in the folder view and a photo in the viewer', async () => {
    await start();
    let opened = '';
    fixture.componentInstance.openFolder.subscribe((path) => (opened = path));
    const names = page().querySelectorAll<HTMLButtonElement>('ul.items button.name');
    names[0].click();
    expect(opened).toBe('/Rechnungen');
    names[1].click();
    await fixture.whenStable();
    expect(page().querySelector('app-viewer img')?.getAttribute('src')).toBe(
      '/api/view?path=%2FFotos%2Fstrand.jpg',
    );
  });

  it('shows the files of a tag', async () => {
    await start();
    page().querySelector<HTMLButtonElement>('.tags .tag')!.click();
    http.expectOne('/api/tagged?tag=Urlaub').flush({ tag: 'Urlaub', items: [photo] });
    await fixture.whenStable();
    expect(page().querySelector('.tagged')?.textContent).toContain('strand.jpg');
    [...page().querySelectorAll<HTMLButtonElement>('.tagged button')]
      .find((b) => b.textContent?.includes('Schließen'))!
      .click();
    await fixture.whenStable();
    expect(page().querySelector('.tagged')).toBeNull();
  });

  it('starts with the tag picked in the folder view', async () => {
    await start('Urlaub');
    http.expectOne('/api/tagged?tag=Urlaub').flush({ tag: 'Urlaub', items: [] });
    await fixture.whenStable();
    expect(page().querySelector('.tagged')?.textContent).toContain('Keine Dateien');
  });

  it('removes a favourite', async () => {
    await start();
    page()
      .querySelector<HTMLButtonElement>('button[aria-label="Rechnungen kein Favorit mehr"]')!
      .click();
    const req = http.expectOne('/api/favourites');
    expect(req.request.body).toEqual({ path: '/Rechnungen', favourite: false });
    req.flush({});
    http.expectOne('/api/marks').flush({ tags: [], favourites: [], recent: [] });
    await fixture.whenStable();
    expect(page().textContent).toContain('Noch nichts markiert');
  });
});
