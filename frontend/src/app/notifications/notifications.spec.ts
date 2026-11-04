import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import {
  describe as describeResult,
  NotificationSettings,
  NotificationsView,
} from './notifications';
import { decodeKey, PushDevice } from './push-device';

const settings: NotificationSettings = {
  email: 'chris@example.org',
  events: { disk: { email: true, push: false } },
  kinds: [
    { kind: 'disk', name: 'Platte oder RAID fällt aus', system: true },
    { kind: 'share', name: 'Etwas wurde mit mir geteilt', system: false },
  ],
  mail: true,
  pushKey: 'BAAA',
  devices: ['https://fcm.googleapis.com/fcm/send/other'],
};

/** A browser that can get push messages, without the browser. */
class FakeDevice {
  endpointNow: string | null = null;
  supported = vi.fn(() => true);
  blocked = vi.fn(() => false);
  endpoint = vi.fn(async () => this.endpointNow);
  subscribe = vi.fn(async (): Promise<PushSubscriptionJSON | null> => ({
    endpoint: 'https://fcm.googleapis.com/fcm/send/here',
    keys: { p256dh: 'k', auth: 'a' },
  }));
  unsubscribe = vi.fn(async () => this.endpointNow);
}

describe('NotificationsView', () => {
  let http: HttpTestingController;
  let fixture: ComponentFixture<NotificationsView>;
  let device: FakeDevice;

  async function start(
    answer: NotificationSettings = settings,
    setUp: (d: FakeDevice) => void = () => undefined,
  ): Promise<void> {
    device = new FakeDevice();
    setUp(device);
    TestBed.configureTestingModule({
      providers: [
        provideHttpClient(),
        provideHttpClientTesting(),
        { provide: PushDevice, useValue: device },
      ],
    });
    http = TestBed.inject(HttpTestingController);
    fixture = TestBed.createComponent(NotificationsView);
    http.expectOne('/api/notifications').flush(answer);
    await fixture.whenStable();
  }

  afterEach(() => http.verify());

  function page(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  function button(text: string): HTMLButtonElement {
    return [...page().querySelectorAll('button')].find((b) => b.textContent?.includes(text))!;
  }

  function box(label: string): HTMLInputElement {
    return page().querySelector(`input[aria-label="${label}"]`)!;
  }

  it('shows the choices and saves them', async () => {
    await start();
    expect(box('Platte oder RAID fällt aus per E-Mail').checked).toBe(true);
    expect(box('Platte oder RAID fällt aus per Push').checked).toBe(false);
    box('Etwas wurde mit mir geteilt per Push').click();
    const email = page().querySelector<HTMLInputElement>('input[type=email]')!;
    email.value = ' anna@example.org ';
    email.dispatchEvent(new Event('input'));
    button('Speichern').click();
    const req = http.expectOne('/api/notifications');
    expect(req.request.method).toBe('POST');
    expect(req.request.body).toEqual({
      email: 'anna@example.org',
      events: { disk: { email: true, push: false }, share: { email: false, push: true } },
    });
    req.flush({});
    await fixture.whenStable();
    expect(page().textContent).toContain('Gespeichert');
  });

  it('shows the server error when saving fails', async () => {
    await start();
    button('Speichern').click();
    http
      .expectOne('/api/notifications')
      .flush({ error: 'Ungültige E-Mail-Adresse' }, { status: 400, statusText: 'Bad Request' });
    await fixture.whenStable();
    expect(page().textContent).toContain('Ungültige E-Mail-Adresse');
  });

  it('explains what the server cannot send', async () => {
    await start({ ...settings, mail: false, pushKey: '' });
    expect(box('Platte oder RAID fällt aus per E-Mail').disabled).toBe(true);
    expect(box('Platte oder RAID fällt aus per Push').disabled).toBe(true);
    expect(page().textContent).toContain('verschickt keine E-Mails');
    expect(page().textContent).toContain('verschickt keine Push-Nachrichten');
  });

  it('turns push on for this device', async () => {
    await start();
    button('Auf diesem Gerät einschalten').click();
    await fixture.whenStable();
    expect(device.subscribe).toHaveBeenCalledWith('BAAA');
    const req = http.expectOne('/api/notifications/subscribe');
    expect(req.request.body.endpoint).toBe('https://fcm.googleapis.com/fcm/send/here');
    req.flush({});
    device.endpointNow = 'https://fcm.googleapis.com/fcm/send/here';
    http.expectOne('/api/notifications').flush({
      ...settings,
      devices: [...settings.devices, device.endpointNow],
    });
    await fixture.whenStable();
    expect(page().textContent).toContain('Dieses Gerät bekommt Push-Nachrichten');
    expect(page().textContent).toContain('2 Geräte bekommen');
  });

  it('says when the browser blocks notifications', async () => {
    await start();
    device.subscribe.mockResolvedValueOnce(null);
    device.blocked.mockReturnValue(true);
    button('Auf diesem Gerät einschalten').click();
    await fixture.whenStable();
    expect(page().textContent).toContain('erlaube sie in den');
  });

  it('turns push off for this device', async () => {
    await start(settings, (d) => (d.endpointNow = 'https://fcm.googleapis.com/fcm/send/other'));
    await fixture.whenStable();
    button('Ausschalten').click();
    await fixture.whenStable();
    const req = http.expectOne('/api/notifications/unsubscribe');
    expect(req.request.body).toEqual({ endpoint: 'https://fcm.googleapis.com/fcm/send/other' });
    req.flush({});
    http.expectOne('/api/notifications').flush({ ...settings, devices: [] });
    await fixture.whenStable();
    expect(page().textContent).toContain('Auf diesem Gerät einschalten');
  });

  it('says when the browser cannot get push messages', async () => {
    await start(settings, (d) => d.supported.mockReturnValue(false));
    expect(page().textContent).toContain('Zum Home-Bildschirm');
  });

  it('sends a test message', async () => {
    await start();
    button('Testnachricht senden').click();
    http.expectOne('/api/notifications/test').flush({ email: 'ok', devices: 1, failed: 0 });
    await fixture.whenStable();
    expect(page().textContent).toContain('E-Mail gesendet, Push an 1 Gerät gesendet');
  });
});

describe('describe', () => {
  it('says what a test reached', () => {
    expect(describeResult({ devices: 0, failed: 0 })).toContain('Nichts gesendet');
    expect(describeResult({ email: 'Senden fehlgeschlagen: x', devices: 2, failed: 1 })).toBe(
      'E-Mail: Senden fehlgeschlagen: x, Push an 2 Geräte gesendet, 1 Gerät nicht erreicht',
    );
  });
});

describe('decodeKey', () => {
  it('reads base64url without padding', () => {
    expect([...decodeKey('-_8')]).toEqual([0xfb, 0xff]);
  });
});
