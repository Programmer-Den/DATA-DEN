# DATA-DEN

A self-hosted NAS with a web interface to upload, download and manage files:
pictures, documents, videos and more. It installs as an app (PWA) on Android
and Windows.

```mermaid
flowchart LR
    browser["Browser or PWA"] -- "HTTPS :443" --> pi["Raspberry Pi<br/>gateway (Docker)"]
    pi -- "encrypted" --> vm["Storage VM (Hyper-V)<br/>DATA-DEN, RAID 1"]
```

- **Pi:** the only machine reachable from the internet. It terminates HTTPS
  and forwards to the storage VM, nothing else (rpi-setup task `dataden`).
- **Storage VM:** a Debian VM on the Windows PC with both disks passed
  through, mirrored and encrypted. Windows itself does not see the disks,
  and only the Pi can reach the VM. An optional nightly backup with versioned snapshots
  goes to an external disk or offsite. Setup: [vm/README.md](vm/README.md).
- **DATA-DEN:** this repository, the web interface and its API, as one
  Docker image running in the VM: [deploy/README.md](deploy/README.md).

The server serves the website, `/api/health`, the login, the folder view,
downloads, uploads and a status page.

## Login

The owner's account comes from the settings below. Its password is stored
only as a bcrypt hash:

```bash
cd backend && go run ./cmd/data-den hash-password       # or: docker run --rm -i data-den hash-password
```

Type the password and put the printed hash into the settings, for example a
`.env` next to the compose file (`.env.example` lists them). Put the hash in
single quotes there, `DATA_DEN_PASSWORD_HASH='$2a$12$...'`, or Docker
Compose reads its `$` signs as variables.

| Setting | Meaning |
|---|---|
| `DATA_DEN_USERNAME` | The owner's account name |
| `DATA_DEN_PASSWORD_HASH` | The hash from `hash-password`; without it nobody can log in |
| `DATA_DEN_TRUSTED_PROXY` | The Pi's address; only for requests from it is `X-Forwarded-For` the client's address |
| `DATA_DEN_DATA_DIR` | The data directory, default `/data`; in the storage VM the mirror at `/srv/data-den` |
| `DATA_DEN_TRASH_DAYS` | How many days, 1 to 3650, a deleted item stays in the recycle bin before it is deleted for good; default 30 |
| `DATA_DEN_DISK_WARN_PERCENT` | Warn when less than this share of the disk is free, in percent; default `10` |
| `DATA_DEN_SMTP_HOST`, `DATA_DEN_SMTP_PORT` | The mail server for [notifications](#notifications) by email, such as `smtp.gmx.net` and `587`; without a host no email is sent |
| `DATA_DEN_SMTP_USERNAME`, `DATA_DEN_SMTP_PASSWORD` | The login at the mail server, often an app password |
| `DATA_DEN_SMTP_FROM` | The sender, such as `DATA-DEN <nas@example.org>` |
| `DATA_DEN_PUSH_CONTACT` | The contact the push services see, `mailto:` or `https://`; default the sender above. Without either, no push messages |
| `DATA_DEN_GEOCODER` | The Nominatim server that names the towns of the photos, default `https://nominatim.openstreetmap.org`; `off` sends no positions anywhere |
<<<<<<< HEAD
| `DATA_DEN_VISION` | Immich's machine learning service on the VM, such as `http://127.0.0.1:3003`, which recognises faces and things in the photos ([deploy/README.md](deploy/README.md)); without it none are recognised |
=======
>>>>>>> origin/claude/project-thread-dlouur
| `DATA_DEN_TILES` | The tile server that draws the photo map (**Karte**), default `https://tile.openstreetmap.org`; `off` fetches no tiles |
| `LISTEN_ADDR` | Where the server listens, default `:8080` |
| `DATA_DEN_TLS_CERT`, `DATA_DEN_TLS_KEY` | Certificate and key in PEM files: with both the server speaks HTTPS, without them plain HTTP |

A login lasts 7 days, in an `HttpOnly`, `Secure`, `SameSite=Strict` cookie;
sessions live in memory, so a restart logs out. After 5 failed logins from
one address within 15 minutes, that address waits until the 15 minutes are
over. Each failed login is logged as `login failed ip=<address>` for
fail2ban on the Pi.

## Accounts

The owner is always an admin. Admins add further accounts in the
**Benutzer** tab: a name (a-z, 0-9, `.`, `-`, `_`), a first password (at
least 10 characters), whether it is an admin too, and a quota in GB (empty
for no limit). They can change the quota and admin role, set a new password
and delete an account later; the owner's account can only be changed in the
settings. In the **Konto** tab everyone sees how much they store of their
quota and changes their own password; a new password logs out the other
devices.

Each account has its own space and sees no one else's files. The owner's
space is the data directory itself, so the files from before there were
accounts stay where they are; every other account gets
`.data-den-users/<name>` in it, which the owner never sees. The recycle bin
counts towards the quota, thumbnails do not. The search, the recycle bin
and the share links work within each account's own space. An upload that would go over
the quota is refused before it starts. Deleting an account moves its folder
to `.data-den-users/.deleted/<name>-<time>`, so its files can still be
recovered in the VM.

The accounts are kept in `.data-den-accounts/accounts.json` in the data
directory, readable only by the server, with bcrypt hashes of the passwords.

### Two-factor login

Each account can turn on a second factor for itself in the **Konto** tab:
scan the QR code with an authenticator app (Aegis, Google Authenticator,
2FAS and the like), type the code it shows, and note the ten recovery codes
shown once then. From then on the login also asks for the app's code
(TOTP, 6 digits, 30 seconds); a recovery code works instead, once each.
Wrong codes count as failed logins. Turning it off again needs the password
and a code.

When someone loses the phone and the recovery codes, an admin turns it off
for that account in the **Benutzer** tab. The owner's own is turned off in
the VM, with the server stopped:

```bash
sudo docker compose stop data-den
sudo docker compose run --rm data-den reset-2fa <username>
sudo docker compose start data-den
```

The secrets are stored in the accounts file, the recovery codes only as
SHA-256 hashes.

### App passwords

Scripts, apps and network drives should not use the account's password and
second factor. Each account creates app passwords for them in the **Konto**
tab, with a name, optionally read-only and with an end date, confirmed with
the password. The token (`dd_` and 43 characters) is shown once; the server
keeps only its SHA-256 hash. Send it as a header, or with Basic auth as the
password next to the account's username (curl asks for it with `-u <username>`):

```bash
curl -H "Authorization: Bearer $DATA_DEN_TOKEN" https://<host>/api/files
```

It works for everything a login can do with files, but not for the
account's settings (password, 2FA, app passwords) or for managing accounts.
A read-only one only reads (GET, HEAD, OPTIONS and PROPFIND). Each can be revoked in the
**Konto** tab; an admin can revoke all of an account's in the **Benutzer**
tab, and deleting an account revokes its own. Wrong tokens count as failed
logins.

### Network drive (WebDAV)

Each account's space is also served over WebDAV at `https://<host>/dav/`,
so Windows, macOS, Linux file managers or rclone can mount it as a drive.
It only takes app passwords (username and app password), never the
account's own password. The same rules hold as in the web interface: the
server's own folders stay hidden, deleting moves into the recycle bin,
overwriting keeps the old file as a version, and the quota counts.

On Windows, in Explorer: **Dieser PC → Netzlaufwerk verbinden**, folder
`https://<host>/dav/`, check **Verbindung mit anderen Anmeldeinformationen
herstellen**, then enter the username and the app password. Windows limits
WebDAV downloads to 50 MB by default; for larger videos raise
`FileSizeLimitInBytes` under
`HKLM\SYSTEM\CurrentControlSet\Services\WebClient\Parameters` (for
example to `0xFFFFFFFF`, 4 GB) and restart the WebClient service.

### Audit log

The **Protokoll** tab shows who did what, when and from which address:
logins (also failed ones and wrong codes), logouts, password and 2FA
changes, accounts created, changed or deleted, and changes to files
(uploads, new folders, moves and renames, the recycle bin) and share links
(created, deleted, files fetched through a link, wrong link passwords).
Admins see everyone's entries and can filter by user; everyone else sees
their own, and what other accounts did in the folders they share with them
(without their IP address).
Everyone can narrow the list to a kind of entry (changed files, deleted and
restored, sharing, logins, accounts) and to a file or folder name, and load
older entries page by page. Viewing and downloading within one's own space is
not logged.

The log is `.data-den-audit/audit.log` in the data directory, one JSON line
per entry, readable only by the server. At 5 MB it moves to `audit.log.1`,
replacing the older one, so the latest 5 to 10 MB are kept.

## Files

After the login the website has four tabs. **Dateien** shows the folders of
the data directory, with a path bar to go back up; a click on a photo, video,
song, PDF or text file opens it in the viewer, other files and the ⬇ link
download it. **Fotos** shows every photo as a timeline by the month it was
taken, with a row of years to jump to, **Videos**
every video, **Dokumente** every document with its folder; all newest first,
with a search over the path and 120 at a time. The viewer goes through the
list with ‹ › or the arrow keys, plays videos with seeking, shows PDFs in a
frame and text files up to 1 MB, and closes with Escape. Downloads can be
resumed (HTTP range requests), which matters for large videos. Every access
goes through Go's `os.Root`, so neither `..` nor a symlink reaches anything
outside the data directory; symlinks are not listed at all. In Docker the
server runs as user 65532, so the data directory has to belong to it:
`sudo chown -R 65532:65532 /srv/data-den`.

Photos, videos, music and documents get their own views. `/api/view` serves
a file for the browser to show, with range requests so a video can seek; it
only takes the formats listed in `backend/internal/files/view.go` (images,
MP4/WebM/MOV/MKV, common audio, PDF and plain text), never HTML, and serves
everything but PDFs in a CSP sandbox. Other files, such as Word or Excel
documents, are downloaded. `/api/media?kind=photo|video|audio|document` lists
every file of that kind in the whole data directory, newest first. Photos
come with `taken` (from the EXIF date, in the camera's local time, such as
`2026-07-14T15:32:10`) and `lat`/`lon` (from the EXIF GPS data) and are sorted
by `taken`, or by the file's time when a photo has no date. Only JPEGs carry
EXIF that is read; it is read once per photo and kept in memory until the
photo changes, ahead of time by the search index's walk.

<<<<<<< HEAD
Above the gallery, **Zeitleiste**, **Karte**, **Orte**, **Reisen**,
**Personen** and **Dinge** switch between the timeline, the map and albums. Each photo with a GPS position gets `place`, the
=======
Above the gallery, **Zeitleiste**, **Karte**, **Orte** and **Reisen** switch
between the timeline, the map and albums. Each photo with a GPS position gets `place`, the
>>>>>>> origin/claude/project-thread-dlouur
town it was taken in, from OpenStreetMap's reverse geocoding (Nominatim,
`DATA_DEN_GEOCODER`). The position is rounded to 0.01° (about a kilometre)
before it is sent, each rounded position is asked for only once, at most one
request a second as Nominatim's usage policy asks, and the answers are kept
in `.data-den-places/names.json`; a new photo gets its town a moment after it
is first listed. The Orte view credits the names to the OpenStreetMap
contributors, as their licence (ODbL) asks. **Orte** has an album per town,
most photos first. **Reisen** finds trips: photos taken away from home (the
town with the most photos), ended by a photo at home or three days without
one; at least three photos make a trip. These albums are made in the browser
from `/api/media`, nothing is stored.

<<<<<<< HEAD
**Personen** and **Dinge** show who and what is in the photos, recognised on
the NAS itself by Immich's machine learning service (`DATA_DEN_VISION`, a
second container next to DATA-DEN: [deploy/README.md](deploy/README.md)). In
the background each account's photos (JPEG, PNG, GIF, WebP, BMP) are sent
to it one at a time, made smaller to 1440 pixels and turned as shown; nothing
leaves the NAS. It finds the faces (InsightFace's `buffalo_l`) and compares
the photo with a short text for each thing (CLIP `ViT-B-32__openai`), such
as Hunde, Strand, Berge, Essen or Dokumente (`backend/internal/vision/labels.go`).
A face goes to the person whose faces it is most like (cosine of at least
0.5), or else becomes a new person; a person is listed once they are in
three photos, or when named. **Benennen** names a person, and **Dieselbe
Person wie …** merges two that the recognition took for different people.
Each account keeps what was found in `.data-den-people/people.json`, with
each photo's size and time, so a photo is recognised once and again only
when it changes or the models change; the faces' pictures are kept with the
thumbnails. API: `GET /api/people` → `{"enabled", "recognised", "waiting",
"people": [{"id", "name", "photos"}]}`, `GET /api/people/face?id=`,
`POST /api/people/rename {"id", "name"}`,
`POST /api/people/merge {"id", "into"}`; `/api/media` gives each photo its
`people` (ids) and `labels`.

=======
>>>>>>> origin/claude/project-thread-dlouur
**Karte** shows the photos with a GPS position on a map of OpenStreetMap:
photos close together are one pin with the newest photo and their number, and
a click on a pin shows its photos. Dragging, the mouse wheel, a pinch, the
arrow keys and + and − move and zoom the map; ⤢ shows all photos again. The
browser only talks to DATA-DEN: the server fetches each tile
(`GET /api/tiles/{z}/{x}/{y}`, behind the login) from `DATA_DEN_TILES` at
most two at a time, with a name for the program as OpenStreetMap's tile usage
policy asks, and keeps it for 30 days in `.data-den-tiles/`, so the tile
server never sees the users' addresses and the website keeps its strict
Content-Security-Policy. The tile server does learn which parts of the map
are looked at. When it cannot be reached the old tile is shown, and with
`off` the pins stand on an empty map. Deleting `.data-den-tiles/` is safe and
only costs fetching the tiles again.

**Eigene Alben** are put together by hand: **Neues Album** creates one, and
**Auswählen** in the timeline or any album chooses photos to add to it,
without moving or copying them. In an own album, chosen photos can be taken
out again (the files stay), and the album renamed or deleted (the photos
stay). Each account keeps its albums in `.data-den-albums/albums.json` in its
space, as the paths of the files; moving a file or folder takes them along,
and a file in the recycle bin is back in its albums when restored. API:
`GET /api/albums`, `POST /api/albums {"name"}`,
`POST /api/albums/rename {"id", "name"}`, `POST /api/albums/delete {"id"}`,
`POST /api/albums/add {"id", "paths"}` (photos and videos only),
`POST /api/albums/remove {"id", "paths"}`.

The gallery shows thumbnails from `/api/thumb`: a JPEG of at most 320 pixels,
made from JPEG, PNG, GIF, WebP and BMP photos up to 100 megapixels, turned by
the photo's EXIF orientation. The first request makes it, two at a time, and
keeps it in `.data-den-thumbs/` in the data directory, hidden like
`.data-den-uploads/`; a changed photo gets a new one. Other photos, such as
HEIC, are shown as they are. Thumbnails of deleted photos stay; deleting
`.data-den-thumbs/` is safe and only costs making them again.

In the folder view, **Neuer Ordner** creates a folder in the open folder;
each row can be renamed (✎), moved (↪: open the target folder, then
**Hierher verschieben**) and deleted (🗑). The API is
`POST /api/folders {"path"}`, `POST /api/move {"from", "to"}` and
`POST /api/delete {"path"}`, JSON only.

Text and Markdown files (TXT, MD, CSV and LOG, UTF-8, up to 1 MB) can be
edited in the browser: **Bearbeiten** in the viewer opens the editor,
**Neue Textdatei** in the folder view makes an empty one (a name without one
of these extensions gets `.md`). Markdown is shown formatted, in the viewer
and next to the text in the editor (Text, Beides, Vorschau). **Speichern** or
Ctrl+S saves; the first save of each editing session keeps the file as it
was as a version (🕘 **Ältere Versionen**), so a wrong edit can be undone.
Text files in folders that another account shares cannot be edited yet. A file changed
elsewhere meanwhile is never overwritten: the editor says so and offers to
load it again. The API is `GET /api/text?path` → `{"content", "version"}` and
`PUT /api/text {"path", "content", "version", "backup"}`, JSON only; an empty
`version` creates a new file and never overwrites one. A save is written to
`.data-den-edits/` first and then replaces the file in one step.

Office documents (Word, Excel, PowerPoint and OpenDocument) can be edited
too, with Collabora Online in a second container in the VM:
[deploy/README.md](deploy/README.md#office-documents-optional). It is
switched on with `DATA_DEN_OFFICE_URL` (where DATA-DEN reaches Collabora,
such as `http://127.0.0.1:9980`) and `DATA_DEN_WOPI_ADDR` (DATA-DEN's port
for Collabora to load and save documents, `127.0.0.1:9981` unless set); the
API is `GET /api/office` → `{"extensions"}` and `POST /api/office {"path"}`
→ the editor's address and a token. Without them office documents are only
downloaded.

Deleting moves the file or folder into the recycle bin,
`.data-den-trash/` in the data directory (hidden; a rename, so quick even for
large folders), and the folder view offers **Rückgängig** right away. The
**Papierkorb** tab lists what was deleted, where it was and until when it
stays; each item can be restored to its old place (its folder is made again
if needed, an item already there is never overwritten) or deleted for good,
and the whole bin emptied. After 30 days (`DATA_DEN_TRASH_DAYS`) an item is
deleted for good, checked at start, every hour and whenever the bin is opened. API: `GET /api/trash`,
`POST /api/trash/restore {"id"}`, `POST /api/trash/delete {"id"}`,
`POST /api/trash/empty {}`. Nothing is ever overwritten by accident
("Existiert bereits"), a folder cannot move into itself, and the data
directory itself and the `.data-den-*` folders cannot be changed.

File versions: an upload onto an existing file stops with **existiert
schon** and asks: **Ersetzen**, **Überspringen**, or **Alle ersetzen**.
Replacing keeps the old content as a version in `.data-den-versions/`
(hidden; a hard link, then a rename, so the file is never half written).
**Ältere Versionen** (🕘 on a file's row) lists them with the time of the
content and of the replacement; each can be downloaded, restored (the
content it replaces becomes a version itself, so nothing is lost) or
deleted. A file keeps its 10 newest versions, each for 30 days, checked at
start and every hour; versions follow the file when it is moved or renamed
and count towards the account's quota. API: `POST /api/uploads` with
`"replace": true`, `GET /api/versions?path=`,
`GET /api/versions/download?path=&id=`, `POST /api/versions/restore {"path", "id"}`,
`POST /api/versions/delete {"path", "id"}`.

The search field above the tabs looks through everything at once and lists
the hits with their folder, the searched words marked; a click opens a file
in the viewer, or its folder in **Dateien** when the browser cannot show it.
`GET /api/search?q=` searches the names and paths of all files and folders
and the text of documents: TXT, MD, CSV and LOG (UTF-8 or Latin-1), PDF,
Word (`.docx`) and OpenDocument (`.odt`). Every word must match; a match in
the name ranks above one in the path, which ranks above one in the text,
and a text match comes with a snippet around it. The index lives in memory:
built in the background at start and refreshed every 10 minutes, reading
again only files whose size or modification time changed. Of each document
up to 50 MB, the first 256 KB of text are indexed. The recycle bin and the
other `.data-den-*` folders are left out.

Every account keeps its own tags, favourites and recently used files, in
`.data-den-marks/marks.json`. On a row of **Dateien**, ☆ marks a file or
folder as a favourite and 🏷 sets its tags (comma-separated, up to 20 of up to
40 characters each); the tags are shown under the name, and a click on one
lists its files. The **Favoriten** tab shows all tags with how many files
have them, the favourites, newest first, and the last 50 files that were
opened, downloaded or uploaded. Moves and the recycle bin keep the marks;
deleting for good forgets them, and share links never show them. API:
`GET /api/marks`, `GET /api/tagged?tag=`, `POST /api/tags {"path", "tags"}`,
`POST /api/favourites {"path", "favourite"}`.

Share links (🔗 on a row) give a file or folder to people without a login:
the link `/s/<id>` works for 1, 7, 30 or 90 days or a year and can ask for a
password. Behind it, a shared folder can be browsed, each file downloaded
and any folder in it downloaded as one ZIP (stored, not compressed, so it
starts at once); nothing outside the shared folder is reachable, not even
through symlinks. A wrong password counts like a failed login (same lockout,
same `login failed` log line for fail2ban, with `share=<id>`); the right one
sets a cookie for that one link, valid for a day at most. The **Freigaben**
tab lists the links, until when they work and whether they need a password,
copies a link again and revokes it. Moving or renaming keeps the links
working; in the recycle bin, the link stops working until it is restored.
Expired links answer like unknown ones and are removed. Each link is a file
in `.data-den-shares/` in the data directory (hidden), with the password as
a bcrypt hash. API: `GET /api/shares`,
`POST /api/shares {"path", "days", "password", "upload"}`,
`POST /api/shares/delete {"id"}`; for the link `GET /api/s/{id}`,
`POST /api/s/{id}/unlock {"password"}`, `GET /api/s/{id}/files?path=`,
`GET /api/s/{id}/download?path=` and `GET /api/s/{id}/zip?path=`.

An upload link ("Nur hochladen" when sharing a folder) works the other way:
whoever has it can drop files into that folder, in chunks like an upload
behind the login, but sees nothing of what is in it and cannot download.
Only file names are taken, no subfolders; a name that is taken gets the next
free one, such as `a (1).jpg`, so nothing is overwritten. The files count
against the quota of the account that made the link, and each one is in the
audit log as `share-upload`. Expiry and password work as for other links.
API: `POST /api/s/{id}/uploads {"path": "/a.jpg", "size"}`, then
`PUT /api/s/{id}/uploads/{upload}` and `GET /api/s/{id}/uploads/{upload}`
as for `/api/uploads`.

A folder can also be shared with another account (👥 on a folder's row):
to read only, or also to change it (upload, new folders, rename, move,
delete). The other account finds it under **Geteilt mit mir** and works in
it as in its own space, but cannot go above the shared folder or follow a
symlink out of it. What it uploads counts against the owner's quota, what it
deletes goes into the owner's recycle bin, and the owner's versions, search,
links and checks stay the owner's. The **Freigaben** tab lists the folders
one shares, to end them; the other account can leave one. Sharing a folder
again with the same account changes the rights. Moving or renaming keeps a
shared folder working, deleting an account ends its shared folders, and the
other account is notified (event `share`). The audit log names the account
that worked in the folder, with the owner before the path (`chef:/Projekt/a.jpg`).
All shared folders are in `.data-den-grants.json` at the top of the data
directory. API: `GET /api/accounts`, `GET /api/grants` (`mine`, `withMe`),
`POST /api/grants {"path", "to", "write"}`, `POST /api/grants/delete {"id"}`;
within a shared folder the usual `/api/files`, `/api/download`, `/api/view`,
`/api/media`, `/api/thumb`, `/api/uploads`, `/api/folders`, `/api/move` and
`/api/delete` with `?grant=<id>`, every path within the folder.

Team folders (**Team-Ordner** tab, admins only) belong to no account: an
admin creates one, adds accounts as members, each to read only or also to
change, and removes them again. Members find it under **Geteilt mit mir** and
use it like a shared folder; those who may change it can also restore from
its own recycle bin, since no owner is there to do it. A team folder has no
quota and lives in `.data-den-users/.teams/<name>`; deleting it moves that
aside to `.data-den-users/.deleted/.team-<name>-<time>`. Members are told
(event `share`) and every change is in the audit log (`team-*`, and the
members' work as `@<name>:/path`). API: `GET /api/teams`,
`POST /api/teams {"name"}`, `POST /api/teams/{name}/delete`,
`POST /api/teams/{name}/members {"to", "write"}`,
`POST /api/teams/{name}/members/delete {"to"}`.

Every file gets a SHA-256 checksum, kept in `.data-den-sums/index.json` in
the data directory (hidden like the other `.data-den-*` folders; deleting it
is safe and only costs reading every file again). An upload's checksum is
taken from the bytes as they arrive, and the last chunk's answer lists the
files that already have the same content, which the upload list shows.
Every hour the server walks the data directory: new and changed files (other
size or time) get a checksum, and every file is read again at least every
30 days. A file whose content changed while its size and time did not is
damaged on the disk: RAID 1 keeps both disks equal, not correct. The log
says so as an error, and the **Prüfung** tab lists it; once the file is put
back from a copy, or looked at and found fine, **Als richtig übernehmen**
takes its content as it is. The same tab starts a check of every file now,
shows its progress, and lists the duplicates, most space to gain first, each
with 🗑 to move one into the recycle bin. Moves and the recycle bin keep the
checksums. Each account has its own checksums and sees only its own
duplicates; the checks of all accounts read one file at a time. API:
`GET /api/duplicates`, `GET /api/integrity`,
`POST /api/integrity/check {}`, `POST /api/integrity/accept {"path"}`.

Uploads take many files or whole folders at once, picked with the two
buttons or dragged onto the page, into the open folder. Three files upload
at a time, each with its own progress bar. Every file goes in chunks of
8 MB (`/api/uploads`); a failed chunk is sent again from where the server
stands, so a broken connection costs at most one chunk. Folders in the path
are created, and an existing file is never overwritten: the upload stops
with "Datei existiert bereits". Unfinished chunks wait in
`.data-den-uploads/` in the data directory, which the folder view hides; a
restart of the server removes them.

## Android app

The PWA cannot upload in the background, so [android/](android/README.md)
holds a small native app that uploads new photos and videos from the phone's
camera on its own, with an app password instead of the account password.

## Desktop sync

`data-den-sync` keeps a folder on a PC and a folder in DATA-DEN the same,
both ways, like a sync client: a file changed, added or deleted on either
side follows on the other, over the same HTTPS address as the website. It
is built for Windows (x64 and ARM) into the image; download it while logged
in from `https://<address>/api/sync/client/data-den-sync-windows-amd64.exe`
(or `-arm64.exe`), or build it with
`cd backend && GOOS=windows go build ./cmd/data-den-sync`.

```powershell
.\data-den-sync.exe setup            # address, app password, folder on the PC, folder in DATA-DEN
.\data-den-sync.exe run              # syncs now and every minute; Ctrl+C stops
.\data-den-sync.exe autostart on     # starts "run --hidden" at every login, for this user only
```

It logs in with an app password, never the account's password; on Windows
the password is kept encrypted for that Windows user (DPAPI), elsewhere in a
file only the user can read, or comes from `DATA_DEN_SYNC_TOKEN`. Settings
and the log (`sync.log`) are in `%AppData%\DATA-DEN-Sync`; `interval` in
`config.json` sets the seconds between rounds (default 60).

Each round lists both sides (`GET /api/sync/tree?path=`, every file with its
size, time and checksum) and compares them with the state after the last
round, kept in `.data-den-sync\state.json` in the synced folder:

- Changed on one side: copied to the other. A file the server replaces is
  kept as a version, one it deletes goes into its recycle bin; one deleted
  on the PC is moved to `.data-den-sync\deleted\<day>` and removed after
  30 days.
- Changed on both sides: the PC's version becomes
  `name (Konflikt <date> <PC>).ext` and is uploaded too, so nothing is
  lost; the server's version keeps the name. Equal content is no conflict.
- Deleted on one side, changed on the other: the change wins.
- A folder deleted on one side is deleted as a whole on the other, unless
  something in it changed there.

The server replaces or deletes a file only while it is still as the round saw
it (`"replace": true, "expect": {"size", "mod"}` on `POST /api/uploads`,
`"size"` and `"mod"` on `POST /api/delete`, otherwise 412), so an edit from another device in the
meantime waits for the next round instead of being overwritten. If one side
is suddenly empty while both had files, such as a missing drive, the client
stops instead of deleting everything. Names Windows cannot hold (`:`, `?`,
`CON` and the like) and names that differ only in case are left out with a
line in the log; `desktop.ini`, `Thumbs.db`, `~$` Office lock files and
`.tmp` files are never synced.

## Status

The **Status** tab shows the free space on the data disk, the state of the
RAID and how long the server and the VM have been up. `GET /api/status`
(behind the login) reads the space with `statfs` on the data directory and
the RAID from `/proc/mdstat`, which a container shares with its VM. When less
than `DATA_DEN_DISK_WARN_PERCENT` (10 %) of the disk is free, or a disk of
the mirror is missing, every page shows a warning; below a quarter of that
(2.5 %) the warning says the disk is almost full. The web interface checks
every 5 minutes while someone is logged in. Outside the VM there is no RAID,
and the page says so. `/api/health` stays as it is, without login, for
Docker's health check.

## Notifications

In the **Benachrichtigungen** tab each account chooses which events reach it
by email and which by push message:

| Event | Who | When |
|---|---|---|
| Disk or RAID failing | admins | A disk of the mirror fails or the RAID stops, and again when it is whole; Windows finds a disk's SMART health bad ([vm/README.md](vm/README.md#disk-health)) |
| Backup failed | admins | The nightly backup reports a failure |
| Low disk space | admins | The free space falls below `DATA_DEN_DISK_WARN_PERCENT`, and again below a quarter of it |
| Damaged files | admins | A check finds files whose content changed on the disk |
| Shared with me | everyone | Someone shares a file or folder with the account |

Emails go out over the mail server from the `DATA_DEN_SMTP_` settings,
always encrypted: port 465 speaks TLS from the start, any other port must
offer STARTTLS. Push messages use Web Push: **Auf diesem Gerät einschalten**
asks the browser for permission and registers `sw.js`, which shows them. It
works in Chrome, Edge, Firefox and Safari; on an iPhone or iPad only when
DATA-DEN was added to the home screen. The server signs push messages with
a key it makes on its first start, `.data-den-notify/vapid.pem` in the data
directory, next to the settings in `settings.json`; it only sends to the
push services of Google, Mozilla, Apple and Microsoft. **Testnachricht
senden** tries every way an account set up.

The server checks the RAID and the free space every minute. The VM reports
a failed backup (`vm/backup-result.sh`) or a disk Windows finds bad (`vm/disk-health.sh`) as a JSON file in `.data-den-events/` in
the data directory, which the server sends and deletes:

```json
{"kind": "backup", "title": "Backup fehlgeschlagen", "body": "What went wrong"}
```

`kind` is `backup` or `disk`. The folder must belong to the image's user
(65532), so the server can delete the files.

## Development

Needs Go (version in `backend/go.mod`) and Node (version in
`frontend/.nvmrc`).

```bash
cd frontend && npm ci && npm run build   # builds the website into the backend
cd ../backend && DATA_DEN_USERNAME=me DATA_DEN_PASSWORD_HASH='<hash>' go run ./cmd/data-den   # http://localhost:8080
```

For live reload of the website, run `npm start` in `frontend/` next to the
backend: `http://localhost:4200`, with `/api` passed to the backend.

Checks, as CI runs them:

```bash
cd backend && gofmt -l . && golangci-lint run && go test ./...
cd frontend && npm run format:check && npm run lint && npm test && npm run build
```

With Docker:

```bash
docker build -t data-den .
docker run --rm -p 8080:8080 data-den
```

## CI

Every pull request runs the shared checks of
[repo-base](https://github.com/Programmer-Den/repo-base) (secrets, links,
PR template), the Go and Angular jobs, tests and builds the Android app, and
builds the image for amd64 and arm64. They run on the light self-hosted runners once those are set up
([runner/README.md](runner/README.md)), otherwise on GitHub's runners.

Contributions follow [AGENTS.md](AGENTS.md); security issues go to
[SECURITY.md](SECURITY.md).
