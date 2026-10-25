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

## 2. Get the files

```bash
mkdir -p ~/data-den/tls && cd ~/data-den
scp <you>@<your PC>:<repo>/deploy/compose.yaml .     # or copy it over another way
```

## 3. Certificate for the way from the Pi

A self-signed certificate for 10 years is enough: only the Pi talks to the
VM, and it can pin exactly this certificate.

```bash
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
  -days 3650 -subj "/CN=data-den" -addext "subjectAltName=DNS:data-den" \
  -keyout tls/key.pem -out tls/cert.pem
sudo chown 65532 tls/key.pem && sudo chmod 400 tls/key.pem
```

Copy `tls/cert.pem` (never the key) to the Pi for its gateway.

## 4. Settings

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

The data directory, the port and the certificate are set in `compose.yaml`.
The image's user must be able to write to the mirror:

```bash
sudo chown -R 65532:65532 /srv/data-den
```

## 5. Start

```bash
sudo docker compose up -d
sudo docker compose ps                    # "healthy" after a few seconds
curl -k https://localhost:8443/api/health
```

It starts again with the VM. From another device than the Pi, port 8443 is
not reachable.

If `docker compose pull` says *denied* or *unauthorized*, the package is still
private: on GitHub under **Packages › data-den › Package settings › Change
visibility** make it public, or log in once with `sudo docker login ghcr.io`
and a token that may read packages.

## Update

```bash
cd ~/data-den
sudo docker compose pull && sudo docker compose up -d
sudo docker image prune -f
```

`sudo docker compose logs -f` shows what the server does, including the
failed logins.
