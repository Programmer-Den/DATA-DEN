import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { Hit, SearchView, mark } from './search';

const pdf: Hit = {
  path: '/Dokumente/Rechnung 2026.pdf',
  name: 'Rechnung 2026.pdf',
  dir: false,
  size: 2048,
  modified: '2026-11-01T10:00:00Z',
  kind: 'document',
  preview: true,
  snippet: '…die Rechnung über 40 Euro…',
};
const word: Hit = {
  path: '/Dokumente/brief.docx',
  name: 'brief.docx',
  dir: false,
  size: 1024,
  modified: '2026-10-01T10:00:00Z',
  kind: 'document',
};
const folder: Hit = {
  path: '/Rechnungen',
  name: 'Rechnungen',
  dir: true,
  size: 0,
  modified: '2026-09-01T10:00:00Z',
};

describe('mark', () => {
  it('marks every place a word occurs, ignoring case', () => {
    expect(mark('Rechnung und rechnung', ['rechnung'])).toEqual([
      { text: 'Rechnung', hit: true },
      { text: ' und ', hit: false },
      { text: 'rechnung', hit: true },
    ]);
  });

  it('joins overlapping words', () => {
    expect(mark('abcd', ['ab', 'bc'])).toEqual([
      { text: 'abc', hit: true },
      { text: 'd', hit: false },
    ]);
  });

  it('leaves text without hits as it is', () => {
    expect(mark('Urlaub', ['rechnung'])).toEqual([{ text: 'Urlaub', hit: false }]);
  });
});

describe('SearchView', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<SearchView>;

  beforeEach(async () => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    fixture = TestBed.createComponent(SearchView);
    fixture.componentRef.setInput('query', ' Rechnung ');
    await fixture.whenStable();
    http
      .expectOne((r) => r.url === '/api/search' && r.params.get('q') === 'Rechnung')
      .flush({ items: [pdf, word, folder], total: 3, ready: true });
    await fixture.whenStable();
  });

  afterEach(() => http.verify());

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  it('lists the hits with their folder and marks the words', () => {
    expect(page().textContent).toContain('3 Treffer für „Rechnung“');
    const rows = page().querySelectorAll('.hits li');
    expect(rows.length).toBe(3);
    expect(rows[0].textContent).toContain('/Dokumente');
    expect(rows[0].querySelector('.snippet')?.textContent).toContain('die Rechnung über 40 Euro');
    const marks = [...rows[0].querySelectorAll('mark')].map((m) => m.textContent);
    expect(marks).toEqual(['Rechnung', 'Rechnung']);
    expect(rows[2].textContent).toContain('📁');
  });

  it('opens a file the browser can show in the viewer', async () => {
    page().querySelector<HTMLButtonElement>('.hits li .name')!.click();
    await fixture.whenStable();
    expect(page().querySelector('app-viewer')).not.toBeNull();
  });

  it('shows the folder of other files and folders themselves', () => {
    const opened: string[] = [];
    fixture.componentInstance.openFolder.subscribe((path) => opened.push(path));
    const names = page().querySelectorAll<HTMLButtonElement>('.hits li .name');
    names[1].click();
    names[2].click();
    expect(opened).toEqual(['/Dokumente', '/Rechnungen']);
  });

  it('says so while the index is still built', async () => {
    fixture.componentRef.setInput('query', 'urlaub');
    await fixture.whenStable();
    http
      .expectOne((r) => r.params.get('q') === 'urlaub')
      .flush({ items: [], total: 0, ready: false });
    await fixture.whenStable();
    expect(page().textContent).toContain('Nichts gefunden für „urlaub“');
    expect(page().textContent).toContain('Suchindex wird noch aufgebaut');
  });
});
