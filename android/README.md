# DATA-DEN for Android

Uploads new photos and videos from the phone to DATA-DEN in the background,
which the PWA cannot do. Everything else (browsing, sharing, downloads) stays
in the web interface.

## Set up

1. In the web interface, create an app password under the account settings.
   It starts with `dd_` and must be allowed to write (not read-only). It
   works only for files, never for the account itself, and can be revoked
   there without changing the password.
2. Install the app (see below), open it and enter the server's address (for
   example `nas.example.org`; only HTTPS is accepted), the app password and
   the target folder.
3. Allow access to photos and videos, notifications, and the location in
   photos. Without the last one Android removes the place a photo was taken
   from the uploaded file.

The app checks the app password against the server (`GET /api/session`)
before it saves anything. The password is stored encrypted with a key in the
phone's keystore, and the app is left out of Android's backups.

## What it uploads

- New photos (and videos, if switched on) from the camera, that is the
  folder `DCIM` on the phone. Switch off "Nur Kamera-Aufnahmen" to include
  screenshots, messenger pictures and the like.
- Only what was added after connecting, unless "Auch Fotos hochladen, die
  schon auf dem Handy sind" is on; then everything on the phone.
- Into the target folder by year and month taken:
  `/Fotos/Handy/2026/11/IMG_1234.jpg`.
- A file that is already there with the same name and size counts as
  uploaded. Another file with the same name gets a number, `IMG_1234 (2).jpg`.
  Nothing on the server is ever overwritten.

The app remembers what it uploaded (`uploaded.txt` in its private storage)
and never deletes anything on the phone or on the server.

## When it uploads

- A few seconds after a new photo or video appears, through a content
  trigger of Android's WorkManager, and in any case every 6 hours.
- Only in Wi-Fi by default, and if wanted only while charging.
- In chunks of 8 MiB over the API the web interface uses (`POST
  /api/uploads`, `PUT /api/uploads/{id}`), so a broken connection resumes
  where it stopped. Gateway errors (502 to 504, for example while the server
  restarts) are retried with a growing pause.
- With a notification showing the progress, as a foreground service, so a
  long video is not cut off.

When the app password stops working, the space is full, or the photo
permission was taken away, the app stops and shows a notification; it
resumes after the problem is fixed in the app.

## Install

Each [release](https://github.com/Programmer-Den/DATA-DEN/releases) has the
app as `data-den-android-<version>.apk`, signed with the project's own key,
so a newer release installs over an older one and keeps its settings.
Download it on the phone and allow installing from that source. It cannot
replace a debug build (below): remove that one first.

For testing, every CI run builds a debug APK, `data-den-android-debug`, among the run's
artifacts. Download it on the phone and allow installing from that source.
Debug builds of different runs are signed with different keys, so an
update may need the old app removed first, and its settings with it.

## Release signing

The Release workflow (deploy/README.md, Releases) signs the app with a key
kept only in the repository's secrets. Create it once, on any machine with
a JDK, and keep `data-den-release.jks` and its password safe outside the
repository, for example in a password manager: an app signed with another
key cannot update the installed one.

```bash
keytool -genkeypair -v -keystore data-den-release.jks -alias data-den \
  -keyalg RSA -keysize 4096 -validity 10000 -dname "CN=DATA-DEN"
base64 -w0 data-den-release.jks > data-den-release.jks.b64   # macOS: base64 -i data-den-release.jks
```

Then under **Settings › Secrets and variables › Actions › New repository
secret**:

| Secret | Value |
|---|---|
| `ANDROID_KEYSTORE_BASE64` | the content of `data-den-release.jks.b64` |
| `ANDROID_KEYSTORE_PASSWORD` | the keystore password `keytool` asked for |
| `ANDROID_KEY_ALIAS` | `data-den` |
| `ANDROID_KEY_PASSWORD` | the same password (keytool uses the keystore's) |

Delete the `.b64` file afterwards. One key per app: a key shared by several
apps lets each of them claim to be the others' update.

## Development

Needs JDK 17 and the Android SDK (or Android Studio). From this folder:

```bash
./gradlew :core:test            # the upload protocol, on the JVM
./gradlew :app:lintDebug        # Android lint
./gradlew :app:assembleDebug    # app/build/outputs/apk/debug/
```

- `core/`: the client for the API and the chunked, resumable upload, plain
  Kotlin with OkHttp. Its tests run against an in-memory copy of the
  server's upload API.
- `app/`: the screen, the media store scan and the WorkManager jobs.
