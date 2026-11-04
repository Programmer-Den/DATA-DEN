import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { TrashItem, TrashView } from './trash';

const photo: TrashItem = {
  id: 'a1',
  path: '/Fotos/2026/strand.jpg',
  name: 'strand.jpg',
  dir: false,
  size: 2048,
  deleted: '2026-11-01T10:00:00Z',
};
const folder: TrashItem = {
  id: 'b2',
  path: '/Alt',
  name: 'Alt',
  dir: true,
  size: 0,
  deleted: '2026-10-30T10:00:00Z',
};

describe('TrashView', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<TrashView>;

  beforeEach(async () => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    fixture = TestBed.createComponent(TrashView);
    http.expectOne('/api/trash').flush({ days: 30, items: [photo, folder] });
    await fixture.whenStable();
  });

  afterEach(() => {
    http.verify();
    vi.restoreAllMocks();
  });

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  function buttonIn(row: number, text: string): HTMLButtonElement {
    return [...page().querySelectorAll('tbody tr')[row].querySelectorAll('button')].find((b) =>
      b.textContent?.includes(text),
    )!;
  }

  it('lists what was deleted, where it was and until when it stays', () => {
    const rows = page().querySelectorAll('tbody tr');
    expect(rows.length).toBe(2);
    expect(rows[0].textContent).toContain('Fotos/2026');
    expect(rows[0].textContent).toContain('2 KB');
    expect(rows[0].textContent).toContain('01.12.2026');
    expect(rows[1].textContent).toContain('📁 Alt');
    expect(rows[1].textContent).toContain('Start');
  });

  it('restores', async () => {
    buttonIn(0, 'Wiederherstellen').click();
    const req = http.expectOne('/api/trash/restore');
    expect(req.request.body).toEqual({ id: 'a1' });
    req.flush({ path: photo.path });
    await fixture.whenStable();
    http.expectOne('/api/trash').flush({ days: 30, items: [folder] });
    await fixture.whenStable();
    expect(page().querySelector('[role=status]')?.textContent).toContain('wiederhergestellt');
    expect(page().querySelectorAll('tbody tr').length).toBe(1);
  });

  it("shows the server's message when restoring fails", async () => {
    buttonIn(0, 'Wiederherstellen').click();
    http
      .expectOne('/api/trash/restore')
      .flush(
        { error: 'Am alten Ort gibt es schon „strand.jpg“' },
        { status: 409, statusText: 'Conflict' },
      );
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toContain('gibt es schon');
  });

  it('deletes for good only after asking', async () => {
    const confirm = vi
      .spyOn(window, 'confirm')
      .mockReturnValueOnce(false)
      .mockReturnValueOnce(true);
    buttonIn(1, 'Endgültig').click();
    http.expectNone('/api/trash/delete');
    buttonIn(1, 'Endgültig').click();
    expect(confirm).toHaveBeenCalledTimes(2);
    http.expectOne('/api/trash/delete').flush({ path: '/Alt' });
    await fixture.whenStable();
    http.expectOne('/api/trash').flush({ days: 30, items: [photo] });
  });

  it('empties after asking', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    [...page().querySelectorAll('button')].find((b) => b.textContent?.includes('leeren'))!.click();
    const req = http.expectOne('/api/trash/empty');
    expect(req.request.body).toEqual({});
    req.flush({});
    await fixture.whenStable();
    http.expectOne('/api/trash').flush({ days: 30, items: [] });
    await fixture.whenStable();
    expect(page().textContent).toContain('Der Papierkorb ist leer');
  });
});
