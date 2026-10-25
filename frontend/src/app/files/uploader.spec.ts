import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { CHUNK_SIZE, Uploader, droppedJobs, pickedJobs } from './uploader';

/** Lets the uploader's promises move on to their next request. */
const settle = () => new Promise((resolve) => setTimeout(resolve));

function file(name: string, size: number, relative = ''): File {
  const f = new File([new Uint8Array(size)], name);
  if (relative) {
    Object.defineProperty(f, 'webkitRelativePath', { value: relative });
  }
  return f;
}

describe('Uploader', () => {
  let http: HttpTestingController;
  let uploader: Uploader;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    uploader = TestBed.inject(Uploader);
  });

  afterEach(() => http.verify());

  it('uploads a file in chunks', async () => {
    const size = CHUNK_SIZE + 10;
    const done = uploader.upload([{ file: file('video.mp4', size), path: '/video.mp4' }]);
    await settle();

    const start = http.expectOne('/api/uploads');
    expect(start.request.body).toEqual({ path: '/video.mp4', size });
    start.flush({ id: 'u1', offset: 0 });
    await settle();

    const first = http.expectOne('/api/uploads/u1');
    expect(first.request.method).toBe('PUT');
    expect(first.request.headers.get('Upload-Offset')).toBe('0');
    expect((first.request.body as Blob).size).toBe(CHUNK_SIZE);
    first.flush({ offset: CHUNK_SIZE });
    await settle();

    const second = http.expectOne('/api/uploads/u1');
    expect(second.request.headers.get('Upload-Offset')).toBe(String(CHUNK_SIZE));
    expect((second.request.body as Blob).size).toBe(10);
    second.flush({ offset: size });

    await done;
    expect(uploader.items()[0]).toEqual(
      expect.objectContaining({ status: 'fertig', loaded: size, path: '/video.mp4' }),
    );
  });

  it('goes on from where the server stands after a failed chunk', async () => {
    const done = uploader.upload([{ file: file('a.txt', 6), path: '/a.txt' }]);
    await settle();
    http.expectOne('/api/uploads').flush({ id: 'u2', offset: 0 });
    await settle();
    http.expectOne('/api/uploads/u2').error(new ProgressEvent('error'));
    await settle();

    const state = http.expectOne('/api/uploads/u2');
    expect(state.request.method).toBe('GET');
    state.flush({ offset: 4 });
    await settle();

    const rest = http.expectOne('/api/uploads/u2');
    expect(rest.request.headers.get('Upload-Offset')).toBe('4');
    expect((rest.request.body as Blob).size).toBe(2);
    rest.flush({ offset: 6 });

    await done;
    expect(uploader.items()[0].status).toBe('fertig');
  });

  it("marks a file the server refuses with the server's message", async () => {
    const done = uploader.upload([{ file: file('a.txt', 1), path: '/a.txt' }]);
    await settle();
    http
      .expectOne('/api/uploads')
      .flush({ error: 'Datei existiert bereits' }, { status: 409, statusText: 'Conflict' });
    await done;
    expect(uploader.items()[0]).toEqual(
      expect.objectContaining({ status: 'Fehler', error: 'Datei existiert bereits' }),
    );
  });

  it('uploads at most three files at once', async () => {
    const jobs = [1, 2, 3, 4].map((n) => ({ file: file(`${n}.txt`, 1), path: `/${n}.txt` }));
    void uploader.upload(jobs);
    await settle();
    const started = http.match('/api/uploads');
    expect(started.length).toBe(3);
    expect(uploader.items().map((item) => item.status)).toEqual([
      'lädt hoch',
      'lädt hoch',
      'lädt hoch',
      'wartet',
    ]);
    started.forEach((req) =>
      req.flush({ error: 'Datei existiert bereits' }, { status: 409, statusText: 'Conflict' }),
    );
    await settle();
    http
      .expectOne('/api/uploads')
      .flush({ error: 'Datei existiert bereits' }, { status: 409, statusText: 'Conflict' });
    await settle();
  });

  it('clears the finished uploads', async () => {
    const done = uploader.upload([{ file: file('leer.txt', 0), path: '/leer.txt' }]);
    await settle();
    http.expectOne('/api/uploads').flush({ id: 'u3', offset: 0 });
    await done;
    expect(uploader.items().length).toBe(1);
    uploader.clearDone();
    expect(uploader.items().length).toBe(0);
  });
});

describe('paths of picked and dropped files', () => {
  it('puts picked files below the open folder', () => {
    const files = [file('a.jpg', 1), file('b.jpg', 1, 'Urlaub/b.jpg')];
    const list = Object.assign(files, { item: (i: number) => files[i] }) as unknown as FileList;
    expect(pickedJobs(list, '/').map((job) => job.path)).toEqual(['/a.jpg', '/Urlaub/b.jpg']);
    expect(pickedJobs(list, '/Fotos').map((job) => job.path)).toEqual([
      '/Fotos/a.jpg',
      '/Fotos/Urlaub/b.jpg',
    ]);
  });

  it('walks dropped folders', async () => {
    const photo = file('b.jpg', 1);
    const fileEntry = (name: string, f: File) =>
      ({
        name,
        isFile: true,
        file: (ok: (f: File) => void) => ok(f),
      }) as unknown as FileSystemEntry;
    let read = 0;
    const folder = {
      name: 'Urlaub',
      isFile: false,
      createReader: () => ({
        // One batch, then the empty one that ends the folder.
        readEntries: (ok: (entries: FileSystemEntry[]) => void) =>
          ok(read++ === 0 ? [fileEntry('b.jpg', photo)] : []),
      }),
    } as unknown as FileSystemEntry;
    const data = {
      items: [{ webkitGetAsEntry: () => folder }],
      files: [],
    } as unknown as DataTransfer;

    const jobs = await droppedJobs(data, '/Fotos');
    expect(jobs).toEqual([{ file: photo, path: '/Fotos/Urlaub/b.jpg' }]);
  });
});
