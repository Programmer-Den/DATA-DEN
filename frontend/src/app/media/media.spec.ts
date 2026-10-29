import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { Item, Kind } from './item';
import { MediaView, PAGE } from './media';

function photo(i: number, modified: string): Item {
  return {
    path: `/Fotos/p${i}.jpg`,
    name: `p${i}.jpg`,
    size: 1,
    modified,
    kind: 'photo',
    preview: true,
    thumb: true,
  };
}

describe('MediaView', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => http.verify());

  async function start(kind: Kind, items: Item[]): Promise<ComponentFixture<MediaView>> {
    const fixture = TestBed.createComponent(MediaView);
    fixture.componentRef.setInput('kind', kind);
    await fixture.whenStable();
    http.expectOne(`/api/media?kind=${kind}`).flush({ kind, items });
    await fixture.whenStable();
    return fixture;
  }

  function page(fixture: ComponentFixture<MediaView>): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  it('shows photos by month and opens one in the viewer', async () => {
    const fixture = await start('photo', [
      photo(1, '2026-10-20T10:00:00Z'),
      photo(2, '2026-10-01T10:00:00Z'),
      photo(3, '2026-09-15T10:00:00Z'),
    ]);
    const months = [...page(fixture).querySelectorAll('h2')].map((h) => h.textContent);
    expect(months).toEqual(['Oktober 2026', 'September 2026']);
    const thumbs = page(fixture).querySelectorAll<HTMLButtonElement>('.photos button');
    expect(thumbs.length).toBe(3);

    thumbs[2].click();
    await fixture.whenStable();
    expect(page(fixture).querySelector('app-viewer')?.textContent).toContain('3 / 3');
    expect(page(fixture).querySelector('app-viewer img')?.getAttribute('src')).toBe(
      '/api/view?path=%2FFotos%2Fp3.jpg',
    );
  });

  it('shows thumbnails, and the photo itself when there is none', async () => {
    const fixture = await start('photo', [
      photo(1, '2026-10-20T10:00:00Z'),
      { ...photo(2, '2026-10-20T10:00:00Z'), thumb: false },
    ]);
    const imgs = page(fixture).querySelectorAll<HTMLImageElement>('.photos img');
    expect(imgs[0].getAttribute('src')).toBe('/api/thumb?path=%2FFotos%2Fp1.jpg');
    expect(imgs[1].getAttribute('src')).toBe('/api/view?path=%2FFotos%2Fp2.jpg');

    imgs[0].dispatchEvent(new Event('error'));
    await fixture.whenStable();
    expect(page(fixture).querySelector('.photos img')?.getAttribute('src')).toBe(
      '/api/view?path=%2FFotos%2Fp1.jpg',
    );
  });

  it('opens the first photo too', async () => {
    const fixture = await start('photo', [photo(1, '2026-10-20T10:00:00Z')]);
    page(fixture).querySelector<HTMLButtonElement>('.photos button')!.click();
    await fixture.whenStable();
    expect(page(fixture).querySelector('app-viewer')).not.toBeNull();
  });

  it('shows a page at a time', async () => {
    const items = Array.from({ length: PAGE + 5 }, (_, i) => photo(i, '2026-10-20T10:00:00Z'));
    const fixture = await start('photo', items);
    expect(page(fixture).querySelectorAll('.photos li').length).toBe(PAGE);
    const more = page(fixture).querySelector<HTMLButtonElement>('.more')!;
    expect(more.textContent).toContain('5 weitere');
    more.click();
    await fixture.whenStable();
    expect(page(fixture).querySelectorAll('.photos li').length).toBe(PAGE + 5);
    expect(page(fixture).querySelector('.more')).toBeNull();
  });

  it('lists documents with their folder and finds them by name', async () => {
    const modified = '2026-10-20T10:00:00Z';
    const fixture = await start('document', [
      {
        path: '/Steuer/2026/bescheid.pdf',
        name: 'bescheid.pdf',
        size: 2048,
        modified,
        kind: 'document',
        preview: true,
      },
      { path: '/liste.xlsx', name: 'liste.xlsx', size: 10, modified, kind: 'document' },
    ]);
    const rows = page(fixture).querySelectorAll('tbody tr');
    expect(rows.length).toBe(2);
    expect(rows[0].textContent).toContain('Steuer/2026');
    expect(rows[0].textContent).toContain('2 KB');
    expect(rows[1].textContent).toContain('Start');

    const search = page(fixture).querySelector<HTMLInputElement>('input[type=search]')!;
    search.value = 'STEUER';
    search.dispatchEvent(new Event('input'));
    await fixture.whenStable();
    expect(page(fixture).querySelectorAll('tbody tr').length).toBe(1);
    expect(page(fixture).textContent).toContain('1 von 2');
  });

  it('shows videos as tiles', async () => {
    const fixture = await start('video', [
      {
        path: '/urlaub.mp4',
        name: 'urlaub.mp4',
        size: 10,
        modified: '2026-10-20T10:00:00Z',
        kind: 'video',
        preview: true,
      },
    ]);
    expect(page(fixture).querySelector('.videos')?.textContent).toContain('urlaub.mp4');
  });

  it('says when there is nothing', async () => {
    const fixture = await start('video', []);
    expect(page(fixture).textContent).toContain('Keine Videos gefunden');
  });

  it("shows the server's message", async () => {
    const fixture = TestBed.createComponent(MediaView);
    fixture.componentRef.setInput('kind', 'photo');
    await fixture.whenStable();
    http
      .expectOne('/api/media?kind=photo')
      .flush({ error: 'Datenordner nicht verfügbar' }, { status: 503, statusText: 'Unavailable' });
    await fixture.whenStable();
    expect(page(fixture).querySelector('[role=alert]')?.textContent).toContain('nicht verfügbar');
  });
});
