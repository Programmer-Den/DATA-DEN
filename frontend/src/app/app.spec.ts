import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { App } from './app';

describe('App', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => http.verify());

  function render(): HTMLElement {
    const fixture = TestBed.createComponent(App);
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  it('shows the server version', async () => {
    const fixture = TestBed.createComponent(App);
    http.expectOne('/api/health').flush({ status: 'ok', version: '1.2.3' });
    await fixture.whenStable();
    expect(fixture.nativeElement.textContent).toContain('Version 1.2.3');
  });

  it('says when the server cannot be reached', async () => {
    const fixture = TestBed.createComponent(App);
    http.expectOne('/api/health').flush('', { status: 502, statusText: 'Bad Gateway' });
    await fixture.whenStable();
    expect(fixture.nativeElement.textContent).toContain('nicht erreichbar');
  });

  it('shows the title', () => {
    const page = render();
    http.expectOne('/api/health');
    expect(page.querySelector('h1')?.textContent).toBe('DATA-DEN');
  });
});
