import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { OfficeFormats } from './formats';
import { OfficeFrame } from './office-frame';

describe('OfficeFrame', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<OfficeFrame>;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    fixture = TestBed.createComponent(OfficeFrame);
    fixture.componentRef.setInput('path', '/Briefe/antrag.docx');
  });

  afterEach(() => {
    http.verify();
    vi.restoreAllMocks();
  });

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  it('posts the token to Collabora in the frame', async () => {
    const submit = vi.spyOn(HTMLFormElement.prototype, 'submit').mockReturnValue(undefined);
    fixture.detectChanges();
    const req = http.expectOne('/api/office');
    expect(req.request.body).toEqual({ path: '/Briefe/antrag.docx' });
    req.flush({ url: '/browser/abc/cool.html?WOPISrc=x&lang=de', token: 'geheim', ttl: 123 });
    await fixture.whenStable();

    const form = page().querySelector('form')!;
    expect(form.getAttribute('action')).toBe('/browser/abc/cool.html?WOPISrc=x&lang=de');
    expect(form.getAttribute('target')).toBe('office-frame');
    expect(form.querySelector<HTMLInputElement>('[name=access_token]')!.value).toBe('geheim');
    expect(form.querySelector<HTMLInputElement>('[name=access_token_ttl]')!.value).toBe('123');
    expect(page().querySelector('iframe')?.getAttribute('name')).toBe('office-frame');
    expect(submit).toHaveBeenCalledTimes(1);
  });

  it("shows the server's message when it cannot open", async () => {
    fixture.detectChanges();
    http
      .expectOne('/api/office')
      .flush({ error: 'Office nicht erreichbar' }, { status: 502, statusText: 'Bad Gateway' });
    await fixture.whenStable();
    expect(page().querySelector('[role=alert]')?.textContent).toContain('Office nicht erreichbar');
  });

  it("closes with Collabora's close button", async () => {
    fixture.detectChanges();
    http.expectOne('/api/office').flush({ error: 'x' }, { status: 502, statusText: 'Bad Gateway' });
    let closed = false;
    fixture.componentInstance.closed.subscribe(() => (closed = true));
    const message = (data: string, origin = location.origin) =>
      window.dispatchEvent(new MessageEvent('message', { data, origin }));
    message(JSON.stringify({ MessageId: 'UI_Close' }), 'https://anders.example');
    message('kein JSON');
    expect(closed).toBe(false);
    message(JSON.stringify({ MessageId: 'UI_Close' }));
    expect(closed).toBe(true);
  });
});

describe('OfficeFormats', () => {
  it('asks once which documents can be edited', () => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    const http = TestBed.inject(HttpTestingController);
    const formats = TestBed.inject(OfficeFormats);
    expect(formats.canEdit('a.docx')).toBe(false);
    formats.load();
    formats.load();
    http.expectOne('/api/office').flush({ extensions: ['.docx', '.xlsx'] });
    expect(formats.canEdit('Antrag.DOCX')).toBe(true);
    expect(formats.canEdit('bild.jpg')).toBe(false);
    http.verify();
  });
});
