import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { Version, Versions } from './versions';

const older: Version = {
  id: 'a'.repeat(32),
  path: '/Texte/brief 1.txt',
  size: 2048,
  modified: '2026-10-20T08:00:00Z',
  saved: '2026-11-01T09:00:00Z',
};

describe('Versions', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<Versions>;

  beforeEach(async () => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    fixture = TestBed.createComponent(Versions);
    fixture.componentRef.setInput('path', '/Texte/brief 1.txt');
    fixture.componentRef.setInput('name', 'brief 1.txt');
    fixture.detectChanges();
    http
      .expectOne('/api/versions?path=/Texte/brief%201.txt')
      .flush({ days: 30, versions: [older] });
    await fixture.whenStable();
  });

  afterEach(() => http.verify());

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  function button(text: string): HTMLButtonElement {
    return Array.from(page().querySelectorAll('button')).find(
      (b) => b.textContent?.trim() === text,
    )!;
  }

  it('lists the versions with a download link', () => {
    const items = page().querySelectorAll('li');
    expect(items.length).toBe(1);
    expect(items[0].textContent).toContain('2 KB');
    expect(page().querySelector('a')!.getAttribute('href')).toBe(
      '/api/versions/download?path=%2FTexte%2Fbrief%201.txt&id=' + older.id,
    );
  });

  it('restores a version and says so', async () => {
    let restored = false;
    fixture.componentInstance.restored.subscribe(() => (restored = true));
    button('Wiederherstellen').click();
    const req = http.expectOne('/api/versions/restore');
    expect(req.request.body).toEqual({ path: '/Texte/brief 1.txt', id: older.id });
    req.flush({ path: older.path });
    expect(restored).toBe(true);
    http
      .expectOne('/api/versions?path=/Texte/brief%201.txt')
      .flush({ days: 30, versions: [{ ...older, id: 'b'.repeat(32) }] });
    await fixture.whenStable();
    expect(page().querySelectorAll('li').length).toBe(1);
  });

  it('deletes a version', async () => {
    button('Löschen').click();
    http.expectOne('/api/versions/delete').flush({ path: older.path });
    http.expectOne('/api/versions?path=/Texte/brief%201.txt').flush({ days: 30, versions: [] });
    await fixture.whenStable();
    expect(page().textContent).toContain('Keine.');
  });

  it("shows the server's error", async () => {
    button('Wiederherstellen').click();
    http
      .expectOne('/api/versions/restore')
      .flush({ error: 'Dort ist jetzt ein Ordner' }, { status: 409, statusText: 'Conflict' });
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toContain('Ordner');
  });
});
