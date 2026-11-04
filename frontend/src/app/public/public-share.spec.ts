import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { PublicShare } from './public-share';

const id = '0123456789abcdef0123456789abcdef';
const base = '/api/s/' + id;

describe('PublicShare', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<PublicShare>;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    fixture = TestBed.createComponent(PublicShare);
    fixture.componentRef.setInput('id', id);
    fixture.detectChanges();
  });

  afterEach(() => http.verify());

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  it('offers a shared file for download', async () => {
    http
      .expectOne(base)
      .flush({ name: 'notizen.txt', dir: false, size: 2048, expires: '2026-11-08T10:00:00Z' });
    await fixture.whenStable();
    const link = page().querySelector<HTMLAnchorElement>('a[download]')!;
    expect(link.getAttribute('href')).toBe(base + '/download');
    expect(link.textContent).toContain('notizen.txt');
    expect(page().textContent).toContain('2 KB');
    expect(page().textContent).toContain('08.11.2026');
  });

  it('opens folders within a shared folder', async () => {
    http.expectOne(base).flush({ name: 'Fotos', dir: true, expires: '2026-11-08T10:00:00Z' });
    await fixture.whenStable();
    http.expectOne(base + '/files?path=/').flush({
      path: '/',
      entries: [
        { name: '2026', dir: true, size: 0, modified: '2026-11-01T10:00:00Z' },
        { name: 'a b.jpg', dir: false, size: 10, modified: '2026-11-01T10:00:00Z' },
      ],
    });
    await fixture.whenStable();
    const file = [...page().querySelectorAll<HTMLAnchorElement>('a[download]')].find((a) =>
      a.textContent?.includes('a b.jpg'),
    )!;
    expect(file.getAttribute('href')).toBe(base + '/download?path=%2Fa%20b.jpg');
    [...page().querySelectorAll('button')].find((b) => b.textContent?.includes('2026'))!.click();
    http.expectOne(base + '/files?path=/2026').flush({ path: '/2026', entries: [] });
    await fixture.whenStable();
    expect(page().textContent).toContain('Dieser Ordner ist leer');
    const zip = [...page().querySelectorAll<HTMLAnchorElement>('a')].find((a) =>
      a.textContent?.includes('ZIP'),
    )!;
    expect(zip.getAttribute('href')).toBe(base + '/zip?path=%2F2026');
  });

  it('asks for the password first', async () => {
    http
      .expectOne(base)
      .flush(
        { error: 'Passwort nötig', password: true },
        { status: 401, statusText: 'Unauthorized' },
      );
    await fixture.whenStable();
    const password = page().querySelector<HTMLInputElement>('input[type=password]')!;
    password.value = 'falsch';
    page().querySelector('form')!.dispatchEvent(new Event('submit'));
    http
      .expectOne(base + '/unlock')
      .flush({ error: 'Passwort falsch' }, { status: 401, statusText: 'Unauthorized' });
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toContain('Passwort falsch');

    password.value = 'geheim';
    page().querySelector('form')!.dispatchEvent(new Event('submit'));
    const req = http.expectOne(base + '/unlock');
    expect(req.request.body).toEqual({ password: 'geheim' });
    req.flush({ name: 'notizen.txt', dir: false, size: 1, expires: '2026-11-08T10:00:00Z' });
    await fixture.whenStable();
    expect(page().querySelector('form')).toBeNull();
    expect(page().querySelector('a[download]')?.textContent).toContain('notizen.txt');
  });

  it('takes files through an upload link without showing the folder', async () => {
    http
      .expectOne(base)
      .flush({ name: 'Eingang', dir: true, upload: true, expires: '2026-11-08T10:00:00Z' });
    await fixture.whenStable();
    // No listing is asked for.
    http.expectNone(base + '/files?path=/');
    expect(page().textContent).toContain('„Eingang“');
    const input = page().querySelector<HTMLInputElement>('input[type=file]')!;
    const file = new File(['abc'], 'a.jpg');
    Object.defineProperty(input, 'files', { value: [file] });
    input.dispatchEvent(new Event('change'));
    const start = http.expectOne(base + '/uploads');
    expect(start.request.body).toEqual({ path: '/a.jpg', size: 3 });
    start.flush({ id: 'u1', offset: 0 });
    await new Promise((resolve) => setTimeout(resolve));
    const chunk = http.expectOne(base + '/uploads/u1');
    expect(chunk.request.method).toBe('PUT');
    chunk.flush({ offset: 3, name: 'a (1).jpg' });
    await new Promise((resolve) => setTimeout(resolve));
    fixture.detectChanges();
    expect(page().textContent).toContain('fertig');
    expect(page().textContent).toContain('gespeichert als „a (1).jpg“');
    expect(page().querySelector('a[download]')).toBeNull();
  });

  it('says when the link is gone', async () => {
    http
      .expectOne(base)
      .flush({ error: 'Link ungültig oder abgelaufen' }, { status: 404, statusText: 'Not Found' });
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toContain('abgelaufen');
  });
});
