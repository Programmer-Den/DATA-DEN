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
  through and mirrored. Windows itself does not see the disks, and only the
  Pi can reach the VM. An optional nightly backup with versioned snapshots
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

## Files

After the login the website has four tabs. **Dateien** shows the folders of
the data directory, with a path bar to go back up; a click on a photo, video,
song, PDF or text file opens it in the viewer, other files and the ⬇ link
download it. **Fotos** shows every photo as a gallery by month, **Videos**
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
every file of that kind in the whole data directory, newest first.

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

Deleting moves the file or folder into the recycle bin,
`.data-den-trash/` in the data directory (hidden; a rename, so quick even for
large folders), and the folder view offers **Rückgängig** right away. The
**Papierkorb** tab lists what was deleted, where it was and until when it
stays; each item can be restored to its old place (its folder is made again
if needed, an item already there is never overwritten) or deleted for good,
and the whole bin emptied. After 30 days (`DATA_DEN_TRASH_DAYS`) an item is
deleted for good, checked at start, every hour and whenever the bin is opened. API: `GET /api/trash`,
`POST /api/trash/restore {"id"}`, `POST /api/trash/delete {"id"}`,
`POST /api/trash/empty {}`. Nothing is ever overwritten
("Existiert bereits"), a folder cannot move into itself, and the data
directory itself and the `.data-den-*` folders cannot be changed.

`GET /api/search?q=` searches the names and paths of all files and folders
and the text of documents: TXT, MD, CSV and LOG (UTF-8 or Latin-1), PDF,
Word (`.docx`) and OpenDocument (`.odt`). Every word must match; a match in
the name ranks above one in the path, which ranks above one in the text,
and a text match comes with a snippet around it. The index lives in memory:
built in the background at start and refreshed every 10 minutes, reading
again only files whose size or modification time changed. Of each document
up to 50 MB, the first 256 KB of text are indexed. The recycle bin and the
other `.data-den-*` folders are left out.

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
`POST /api/shares {"path", "days", "password"}`,
`POST /api/shares/delete {"id"}`; for the link `GET /api/s/{id}`,
`POST /api/s/{id}/unlock {"password"}`, `GET /api/s/{id}/files?path=`,
`GET /api/s/{id}/download?path=` and `GET /api/s/{id}/zip?path=`.

Uploads take many files or whole folders at once, picked with the two
buttons or dragged onto the page, into the open folder. Three files upload
at a time, each with its own progress bar. Every file goes in chunks of
8 MB (`/api/uploads`); a failed chunk is sent again from where the server
stands, so a broken connection costs at most one chunk. Folders in the path
are created, and an existing file is never overwritten: the upload stops
with "Datei existiert bereits". Unfinished chunks wait in
`.data-den-uploads/` in the data directory, which the folder view hides; a
restart of the server removes them.

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
PR template), the Go and Angular jobs, and builds the image for amd64 and
arm64. They run on the light self-hosted runners once those are set up
([runner/README.md](runner/README.md)), otherwise on GitHub's runners.

Contributions follow [AGENTS.md](AGENTS.md); security issues go to
[SECURITY.md](SECURITY.md).
