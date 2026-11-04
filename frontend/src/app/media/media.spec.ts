import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { OwnAlbum } from './albums';
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

  afterEach(() => {
    http.verify();
    vi.restoreAllMocks();
  });

  async function start(
    kind: Kind,
    items: Item[],
    albums: OwnAlbum[] = [],
  ): Promise<ComponentFixture<MediaView>> {
    const fixture = TestBed.createComponent(MediaView);
    fixture.componentRef.setInput('kind', kind);
    await fixture.whenStable();
    http.expectOne(`/api/media?kind=${kind}`).flush({ kind, items });
    if (kind === 'photo') {
      http.expectOne('/api/albums').flush({ albums });
    }
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

  it('sorts photos into months by the date taken and jumps to a year', async () => {
    const fixture = await start('photo', [
      { ...photo(1, '2026-10-20T10:00:00Z'), taken: '2026-07-14T15:32:10' },
      { ...photo(2, '2026-10-20T10:00:00Z'), taken: '2015-08-01T10:00:00', lat: 52.52, lon: 13.4 },
    ]);
    const months = [...page(fixture).querySelectorAll('h2')].map((h) => h.textContent);
    expect(months).toEqual(['Juli 2026', 'August 2015']);
    expect(page(fixture).querySelector('#year-2015')?.textContent).toBe('August 2015');
    expect(page(fixture).querySelector('.photos button')?.getAttribute('title')).toContain(
      '14.07.2026',
    );

    const years = [...page(fixture).querySelectorAll<HTMLButtonElement>('.years button')];
    expect(years.map((b) => b.textContent?.trim())).toEqual(['2026', '2015']);
    const target = page(fixture).querySelector<HTMLElement>('#year-2015')!;
    const scrolled = new Promise<void>((resolve) => {
      target.scrollIntoView = () => resolve();
    });
    years[1].click();
    await scrolled;
  });

  it('shows a far year by showing more photos', async () => {
    const items = Array.from({ length: PAGE + 5 }, (_, i) =>
      photo(i, i < PAGE + 2 ? '2026-10-20T10:00:00Z' : '2020-01-01T10:00:00Z'),
    );
    const fixture = await start('photo', items);
    expect(page(fixture).querySelector('#year-2020')).toBeNull();
    page(fixture).querySelectorAll<HTMLButtonElement>('.years button')[1].click();
    await fixture.whenStable();
    expect(page(fixture).querySelector('#year-2020')).not.toBeNull();
  });

  it('shows albums by place and opens one', async () => {
    const fixture = await start('photo', [
      { ...photo(1, '2026-10-20T10:00:00Z'), taken: '2026-07-14T15:00:00', place: 'Rom' },
      { ...photo(2, '2026-10-20T10:00:00Z'), taken: '2026-07-12T15:00:00', place: 'Rom' },
      { ...photo(3, '2026-10-20T10:00:00Z'), taken: '2026-01-01T15:00:00', place: 'Berlin' },
      photo(4, '2026-10-20T10:00:00Z'),
    ]);
    const modes = page(fixture).querySelectorAll<HTMLButtonElement>('.modes button');
    modes[1].click();
    await fixture.whenStable();
    const albums = page(fixture).querySelectorAll<HTMLButtonElement>('.albums button');
    expect([...albums].map((a) => a.querySelector('.title')?.textContent)).toEqual([
      'Rom',
      'Berlin',
    ]);
    expect(albums[0].textContent).toContain('2 Fotos · 12.07.2026 – 14.07.2026');
    expect(page(fixture).querySelector('.credit')?.textContent).toContain('OpenStreetMap');

    albums[0].click();
    await fixture.whenStable();
    expect(page(fixture).querySelector('.album-head h2')?.textContent).toBe('Rom');
    expect(page(fixture).querySelectorAll('.photos li').length).toBe(2);
    expect(page(fixture).textContent).toContain('2 von 2');

    page(fixture).querySelector<HTMLButtonElement>('.photos button')!.click();
    await fixture.whenStable();
    expect(page(fixture).querySelector('app-viewer')?.textContent).toContain('1 / 2');
    page(fixture).querySelector<HTMLButtonElement>('app-viewer header button')!.click();
    await fixture.whenStable();

    page(fixture).querySelector<HTMLButtonElement>('.album-head button')!.click();
    await fixture.whenStable();
    expect(page(fixture).querySelectorAll('.albums button').length).toBe(2);
  });

  it('says when there are no trips', async () => {
    const fixture = await start('photo', [photo(1, '2026-10-20T10:00:00Z')]);
    page(fixture).querySelectorAll<HTMLButtonElement>('.modes button')[2].click();
    await fixture.whenStable();
    expect(page(fixture).textContent).toContain('Keine Reisen gefunden');
  });

  it('makes an own album and adds chosen photos to it', async () => {
    const fixture = await start('photo', [
      photo(1, '2026-10-20T10:00:00Z'),
      photo(2, '2026-10-19T10:00:00Z'),
    ]);
    const modes = page(fixture).querySelectorAll<HTMLButtonElement>('.modes button');
    modes[3].click();
    await fixture.whenStable();
    expect(page(fixture).textContent).toContain('Noch keine eigenen Alben');

    const input = page(fixture).querySelector<HTMLInputElement>('.new-album input')!;
    input.value = 'Hochzeit';
    page(fixture).querySelector<HTMLFormElement>('.new-album')!.dispatchEvent(new Event('submit'));
    const create = http.expectOne('/api/albums');
    expect(create.request.method).toBe('POST');
    expect(create.request.body).toEqual({ name: 'Hochzeit' });
    create.flush({ id: 'a1', name: 'Hochzeit', created: '2026-11-04T10:00:00Z', paths: [] });
    await fixture.whenStable();
    expect(page(fixture).querySelector('.albums .meta')?.textContent).toContain('0 Fotos · leer');

    // Choose two photos in the timeline and add them.
    modes[0].click();
    await fixture.whenStable();
    const choose = [...page(fixture).querySelectorAll<HTMLButtonElement>('.bar button')].find((b) =>
      b.textContent?.includes('Auswählen'),
    )!;
    choose.click();
    await fixture.whenStable();
    const tiles = page(fixture).querySelectorAll<HTMLButtonElement>('.photos button');
    tiles[0].click();
    tiles[1].click();
    tiles[1].click();
    await fixture.whenStable();
    expect(page(fixture).querySelector('app-viewer')).toBeNull();
    expect(page(fixture).querySelector('.choose')?.textContent).toContain('1 ausgewählt');
    expect(tiles[0].classList).toContain('selected');

    const add = [...page(fixture).querySelectorAll<HTMLButtonElement>('.choose button')][0];
    add.click();
    const req = http.expectOne('/api/albums/add');
    expect(req.request.body).toEqual({ id: 'a1', paths: ['/Fotos/p1.jpg'] });
    req.flush({
      id: 'a1',
      name: 'Hochzeit',
      created: '2026-11-04T10:00:00Z',
      paths: ['/Fotos/p1.jpg'],
    });
    await fixture.whenStable();
    expect(page(fixture).querySelector('.notice')?.textContent).toContain('1 Foto zu „Hochzeit“');
  });

  it('opens an own album, takes photos out, renames and deletes it', async () => {
    const album: OwnAlbum = {
      id: 'a1',
      name: 'Hochzeit',
      created: '2026-11-04T10:00:00Z',
      paths: ['/Fotos/p1.jpg', '/Fotos/p2.jpg', '/Fotos/gone.jpg'],
    };
    const fixture = await start(
      'photo',
      [
        photo(1, '2026-10-20T10:00:00Z'),
        photo(2, '2026-10-19T10:00:00Z'),
        photo(3, '2026-10-18T10:00:00Z'),
      ],
      [album],
    );
    page(fixture).querySelectorAll<HTMLButtonElement>('.modes button')[3].click();
    await fixture.whenStable();
    page(fixture).querySelector<HTMLButtonElement>('.albums button')!.click();
    await fixture.whenStable();
    // The file that is gone is left out.
    expect(page(fixture).querySelectorAll('.photos li').length).toBe(2);

    const button = (text: string) =>
      [...page(fixture).querySelectorAll<HTMLButtonElement>('button')].find((b) =>
        b.textContent?.includes(text),
      )!;
    button('Auswählen').click();
    await fixture.whenStable();
    page(fixture).querySelector<HTMLButtonElement>('.photos button')!.click();
    await fixture.whenStable();
    button('Aus Album nehmen').click();
    const remove = http.expectOne('/api/albums/remove');
    expect(remove.request.body).toEqual({ id: 'a1', paths: ['/Fotos/p1.jpg'] });
    remove.flush({ ...album, paths: ['/Fotos/p2.jpg'] });
    await fixture.whenStable();
    expect(page(fixture).querySelectorAll('.photos li').length).toBe(1);

    button('Umbenennen').click();
    await fixture.whenStable();
    const input = page(fixture).querySelector<HTMLInputElement>('.album-head input')!;
    input.value = 'Hochzeit 2026';
    page(fixture)
      .querySelector<HTMLFormElement>('.album-head form')!
      .dispatchEvent(new Event('submit'));
    const rename = http.expectOne('/api/albums/rename');
    expect(rename.request.body).toEqual({ id: 'a1', name: 'Hochzeit 2026' });
    rename.flush({ ...album, name: 'Hochzeit 2026', paths: ['/Fotos/p2.jpg'] });
    await fixture.whenStable();
    expect(page(fixture).querySelector('.album-head h2')?.textContent).toBe('Hochzeit 2026');

    vi.spyOn(window, 'confirm').mockReturnValue(true);
    button('Album löschen').click();
    http.expectOne('/api/albums/delete').flush({ id: 'a1' });
    await fixture.whenStable();
    expect(page(fixture).querySelector('.album-head')).toBeNull();
    expect(page(fixture).textContent).toContain('gelöscht');
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
    http
      .expectOne('/api/albums')
      .flush({ error: 'Datenordner nicht verfügbar' }, { status: 503, statusText: 'Unavailable' });
    await fixture.whenStable();
    expect(page(fixture).querySelector('[role=alert]')?.textContent).toContain('nicht verfügbar');
  });
});
