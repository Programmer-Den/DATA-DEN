import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { Editor } from './editor';
import { isMarkdown, markdownHtml } from './markdown';

describe('Editor', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<Editor>;

  async function open(path: string, content: string, fresh = false): Promise<void> {
    fixture = TestBed.createComponent(Editor);
    fixture.componentRef.setInput('path', path);
    fixture.componentRef.setInput('fresh', fresh);
    fixture.detectChanges();
    http.expectOne(`/api/text?path=${path}`).flush({ path, content, version: 'v1' });
    await fixture.whenStable();
  }

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

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  function textarea(): HTMLTextAreaElement {
    return page().querySelector('textarea')!;
  }

  function saveButton(): HTMLButtonElement {
    return [...page().querySelectorAll('button')].find((b) =>
      b.textContent?.includes('Speichern'),
    )!;
  }

  async function type(text: string): Promise<void> {
    textarea().value = text;
    textarea().dispatchEvent(new Event('input'));
    await fixture.whenStable();
  }

  it('saves the text, keeping the old version only the first time', async () => {
    await open('/notiz.txt', 'Hallo');
    expect(textarea().value).toBe('Hallo');
    expect(saveButton().disabled).toBe(true);
    let saved = 0;
    fixture.componentInstance.saved.subscribe(() => saved++);

    await type('Hallo Welt');
    expect(page().textContent).toContain('Ungespeichert');
    saveButton().click();
    const first = http.expectOne('/api/text');
    expect(first.request.method).toBe('PUT');
    expect(first.request.body).toEqual({
      path: '/notiz.txt',
      content: 'Hallo Welt',
      version: 'v1',
      backup: true,
    });
    first.flush({ path: '/notiz.txt', version: 'v2' });
    await fixture.whenStable();
    expect(page().textContent).toContain('Gespeichert');
    expect(saved).toBe(1);

    // Ctrl+S saves too, now without another copy in the recycle bin.
    await type('Hallo Welt!');
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 's', ctrlKey: true }));
    const second = http.expectOne('/api/text');
    expect(second.request.body).toEqual({
      path: '/notiz.txt',
      content: 'Hallo Welt!',
      version: 'v2',
      backup: false,
    });
    second.flush({ path: '/notiz.txt', version: 'v3' });
  });

  it('keeps nothing for a file that was just made', async () => {
    await open('/neu.md', '', true);
    await type('# Neu');
    saveButton().click();
    expect(http.expectOne('/api/text').request.body.backup).toBe(false);
  });

  it('says when the file changed elsewhere and loads it again', async () => {
    await open('/notiz.txt', 'Hallo');
    await type('Meins');
    saveButton().click();
    http
      .expectOne('/api/text')
      .flush(
        { error: 'Die Datei wurde inzwischen geändert' },
        { status: 409, statusText: 'Conflict' },
      );
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toContain('inzwischen geändert');

    vi.spyOn(window, 'confirm').mockReturnValue(true);
    [...page().querySelectorAll('button')]
      .find((b) => b.textContent?.includes('Neu laden'))!
      .click();
    http
      .expectOne('/api/text?path=/notiz.txt')
      .flush({ path: '/notiz.txt', content: 'Deins', version: 'v2' });
    await fixture.whenStable();
    expect(textarea().value).toBe('Deins');
  });

  it('asks before closing with unsaved changes', async () => {
    await open('/notiz.txt', 'Hallo');
    let closed = false;
    fixture.componentInstance.closed.subscribe(() => (closed = true));
    await type('Anders');
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(confirm).toHaveBeenCalled();
    expect(closed).toBe(false);
    confirm.mockReturnValue(true);
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(closed).toBe(true);
  });

  it('shows Markdown formatted next to the text', async () => {
    await open('/liste.md', '# Einkauf\n\n- Milch');
    expect(page().querySelector('.preview h1')?.textContent).toBe('Einkauf');
    [...page().querySelectorAll('.modes button')]
      .find((b) => b.textContent?.includes('Vorschau'))!
      .dispatchEvent(new Event('click'));
    await fixture.whenStable();
    expect(page().querySelector('textarea')).toBeNull();
    expect(page().querySelector('.preview li')?.textContent).toBe('Milch');
  });
});

describe('markdownHtml', () => {
  it('formats Markdown', () => {
    expect(markdownHtml('**fett**')).toContain('<strong>fett</strong>');
    expect(isMarkdown('A.MD')).toBe(true);
    expect(isMarkdown('a.txt')).toBe(false);
  });
});
