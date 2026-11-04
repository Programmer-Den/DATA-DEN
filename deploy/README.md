# Running DATA-DEN in the storage VM

Each push to `main` builds the image for amd64 and arm64 and pushes it to
`ghcr.io/programmer-den/data-den:main` (`.github/workflows/image.yml`); a tag
`v1.2.3` also as `1.2.3` and `latest`. In the storage VM it runs with
`compose.yaml` from this folder:

- on port **8443** with HTTPS, so the way from the Pi is encrypted too
- with the VM's own network instead of a published port, so the VM's
  nftables still let only the Pi in (Docker's rules for published ports would
  skip them)
- with the mirror `/srv/data-den` as its data directory
- read-only, without capabilities, as the image's non-root user (65532)
- with a health check (`data-den healthcheck`, as the image has no curl)

Before this, set up the VM, its RAID 1 and its firewall: [vm/README.md](../vm/README.md).

## 1. Install Docker in the VM

From Debian's own packages:

```bash
sudo apt install docker.io docker-compose      # docker-compose brings `docker compose`
```

## 2. Log in to ghcr.io

The package is private, so the VM logs in once with a token that may only
read packages. On GitHub under **Settings › Developer settings › Personal
access tokens › Tokens (classic)**, create a token with only the scope
`read:packages` and, if you like, an expiry date. Then in the VM:

```bash
sudo docker login ghcr.io -u <your username>    # the token as the password
```

Docker keeps the login in `/root/.docker/config.json`, so pulls and updates
work from then on. When the token expires, create a new one and log in again.

## 3. Get the files

```bash
mkdir -p ~/data-den/tls && cd ~/data-den
scp <you>@<your PC>:<repo>/deploy/compose.yaml .     # or copy it over another way
```

## 4. Certificate for the way from the Pi

A self-signed certificate for 10 years is enough: only the Pi talks to the
VM, and it can pin exactly this certificate.

```bash
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
  -days 3650 -subj "/CN=data-den" -addext "subjectAltName=DNS:data-den" \
  -keyout tls/key.pem -out tls/cert.pem
sudo chown 65532 tls/key.pem && sudo chmod 400 tls/key.pem
```

Copy `tls/cert.pem` (never the key) to the Pi for its gateway.

## 5. Settings

Create the hash of the password and the `.env` with the settings from the
main [README](../README.md#login):

```bash
sudo docker run --rm -i ghcr.io/programmer-den/data-den:main hash-password
nano .env && chmod 600 .env
```

```bash
DATA_DEN_USERNAME=<name>
DATA_DEN_PASSWORD_HASH='$2a$12$...'
DATA_DEN_TRUSTED_PROXY=192.168.1.20      # the Pi's address
```

For notifications by email, add the mail server
([README](../README.md#notifications)), for example:

```bash
DATA_DEN_SMTP_HOST=smtp.gmx.net
DATA_DEN_SMTP_PORT=587
DATA_DEN_SMTP_USERNAME=<address>
DATA_DEN_SMTP_PASSWORD='<app password>'
DATA_DEN_SMTP_FROM='DATA-DEN <address>'
```

The data directory, the port and the certificate are set in `compose.yaml`.
The image's user must be able to write to the mirror:

```bash
sudo chown -R 65532:65532 /srv/data-den
```

## 6. Start

```bash
sudo docker compose up -d
sudo docker compose ps                    # "healthy" after a few seconds
curl -k https://localhost:8443/api/health
```

It starts again with the VM. From another device than the Pi, port 8443 is
not reachable.

If a pull says *denied* or *unauthorized*, the login is missing or its token
has expired: log in again as in step 2.

## Office documents (optional)

With Collabora Online (CODE, LibreOffice in the browser) Word, Excel and
PowerPoint files and their OpenDocument counterparts (DOC, DOCX, ODT, RTF,
XLS, XLSX, ODS, PPT, PPTX, ODP) can be edited in the web interface: 📝 in
the folder view or **Bearbeiten** in the viewer. `compose.office.yaml` adds
it as a second container:

```bash
cd ~/data-den
scp <you>@<your PC>:<repo>/deploy/compose.office.yaml .
sudo docker compose -f compose.yaml -f compose.office.yaml up -d
sudo docker compose -f compose.yaml -f compose.office.yaml logs -f collabora   # "Ready to accept connections"
```

- Collabora listens only on `127.0.0.1:9980` in the VM. The browser reaches
  it through DATA-DEN (`/browser/`, `/cool/`, `/hosting/`, behind the
  login), so neither the Pi's gateway nor the VM's firewall needs anything
  new. Its admin console is never passed on.
- Collabora loads and saves the documents from DATA-DEN's second port,
  `127.0.0.1:9981` (WOPI), with a token for each opened document that holds
  for 10 hours. It never sees the data directory.
- Collabora saves by itself every few minutes and when the document is
  closed. The first save of each opening keeps the file as it was as a
  version (🕘 **Ältere Versionen**). A document changed elsewhere meanwhile is not overwritten.
- It needs about 1 GB of RAM more, with a few documents open; give the VM
  6 GB or more (`-MemoryGB 6` in `vm/New-DataDenVM.ps1`) if it gets tight.

Use the same `-f` files for `pull`, `up`, `ps` and `logs` from then on.
Without `compose.office.yaml`, office documents are only downloaded, as
before.

## Update

```bash
cd ~/data-den
sudo docker compose pull && sudo docker compose up -d
sudo docker image prune -f
```

With office documents: `sudo docker compose -f compose.yaml -f compose.office.yaml pull`
and `... up -d` the same way.

`sudo docker compose logs -f` shows what the server does, including the
failed logins.
