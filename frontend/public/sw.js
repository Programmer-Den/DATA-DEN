// The service worker of DATA-DEN. For now it only shows the push
// notifications the server sends (backend/internal/notify) and opens
// DATA-DEN when one is tapped.
self.addEventListener('push', (event) => {
  let message = { title: 'DATA-DEN', body: '', tag: '' };
  try {
    message = { ...message, ...event.data.json() };
  } catch {
    // A message that is not JSON shows as the title alone.
  }
  event.waitUntil(
    self.registration.showNotification(message.title, {
      body: message.body,
      tag: message.tag || undefined,
      icon: 'icon.svg',
      badge: 'icon.svg',
    }),
  );
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((windows) => {
      const open = windows.find((w) => new URL(w.url).origin === self.location.origin);
      return open ? open.focus() : self.clients.openWindow('/');
    }),
  );
});
