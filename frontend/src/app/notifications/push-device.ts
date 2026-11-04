import { Injectable } from '@angular/core';

/** The service worker that shows the push notifications (public/sw.js). */
export const SERVICE_WORKER = '/sw.js';

/** Web push on this browser: its subscription to DATA-DEN's messages. */
@Injectable({ providedIn: 'root' })
export class PushDevice {
  /** Whether this browser can get push messages at all. */
  supported(): boolean {
    return (
      typeof navigator !== 'undefined' &&
      'serviceWorker' in navigator &&
      typeof window !== 'undefined' &&
      'PushManager' in window &&
      'Notification' in window
    );
  }

  /** Whether the user blocked notifications for DATA-DEN. */
  blocked(): boolean {
    return this.supported() && Notification.permission === 'denied';
  }

  /** The endpoint this browser gets messages at, or null. */
  async endpoint(): Promise<string | null> {
    if (!this.supported()) {
      return null;
    }
    const registration = await navigator.serviceWorker.getRegistration(SERVICE_WORKER);
    const subscription = await registration?.pushManager.getSubscription();
    return subscription?.endpoint ?? null;
  }

  /**
   * Asks for permission and subscribes with the server's key; returns the
   * subscription for the server, or null when the user said no.
   */
  async subscribe(key: string): Promise<PushSubscriptionJSON | null> {
    if ((await Notification.requestPermission()) !== 'granted') {
      return null;
    }
    const registration = await navigator.serviceWorker.register(SERVICE_WORKER);
    await navigator.serviceWorker.ready;
    const old = await registration.pushManager.getSubscription();
    if (old) {
      await old.unsubscribe();
    }
    const subscription = await registration.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: decodeKey(key),
    });
    return subscription.toJSON();
  }

  /** Ends this browser's subscription; returns its endpoint, or null. */
  async unsubscribe(): Promise<string | null> {
    const registration = await navigator.serviceWorker.getRegistration(SERVICE_WORKER);
    const subscription = await registration?.pushManager.getSubscription();
    if (!subscription) {
      return null;
    }
    await subscription.unsubscribe();
    return subscription.endpoint;
  }
}

/** Reads the server's key, base64url as the API sends it. */
export function decodeKey(key: string): Uint8Array<ArrayBuffer> {
  const base64 = key.replace(/-/g, '+').replace(/_/g, '/');
  const raw = atob(base64 + '='.repeat((4 - (base64.length % 4)) % 4));
  return Uint8Array.from(raw, (c) => c.charCodeAt(0));
}
