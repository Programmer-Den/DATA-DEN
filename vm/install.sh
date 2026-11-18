#!/usr/bin/env bash
# Installs DATA-DEN in the storage VM in one guided run: the mirror of the
# two data disks, the firewall, automatic updates, Docker, the login at
# ghcr.io, the certificate, the settings, DATA-DEN itself and deploys from
# Windows. Each step that is already done is skipped, so it can run again
# after a stop. At the end it prints the settings for the Pi. Copy the vm and
# deploy folders into the VM (the repository or a release's deploy zip), then:
#
#   sudo vm/install.sh [--pi PI_ADDRESS] [--dir FOLDER]
#   vm/install.sh --pi-settings FOLDER     (only prints the settings for the Pi)
#
# Guide: vm/README.md#4-guided-install
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
array=/dev/md/data-den
data=/srv/data-den
# The image's non-root user.
image_user=65532
port=8443

usage() {
	echo "Usage: $0 [--pi PI_ADDRESS] [--dir FOLDER] | --pi-settings FOLDER" >&2
	exit 2
}

step() {
	printf '\n== %s\n' "$*"
}

# is_ipv4 ADDRESS: four numbers from 0 to 255.
is_ipv4() {
	local octet='(25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])'
	[[ "$1" =~ ^$octet\.$octet\.$octet\.$octet$ ]]
}

# is_folder FOLDER: an absolute folder without . or .. in it.
is_folder() {
	[[ "$1" =~ ^/[A-Za-z0-9._/-]+$ && "/$1/" != *"/../"* && "/$1/" != *"/./"* ]]
}

# vm_address: the VM's own address in the home network.
vm_address() {
	ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p'
}

# pi_settings FOLDER: the lines for config/rpi-setup.env on the Pi.
pi_settings() {
	local cert="$1/tls/cert.pem" fp vm
	[[ -f "$cert" ]] || { echo "No certificate at $cert; run sudo $0 first." >&2; return 1; }
	fp="$(openssl x509 -noout -fingerprint -sha256 -in "$cert" | sed 's/^.*=//')"
	vm="$(vm_address)"
	[[ -n "$vm" ]] || { echo "The VM's address is unknown: ip -4 route shows no default route." >&2; return 1; }
	printf 'GATEWAY_UPSTREAM=%s\nGATEWAY_UPSTREAM_PORT=%s\nGATEWAY_UPSTREAM_NAME=data-den\nGATEWAY_UPSTREAM_FINGERPRINT=%s\n' "$vm" "$port" "$fp"
}

pi=""
dir=""
while [[ $# -gt 0 ]]; do
	case "$1" in
		--pi)
			[[ $# -ge 2 ]] || usage
			pi="$2"
			shift 2
			;;
		--dir)
			[[ $# -ge 2 ]] || usage
			dir="${2%/}"
			shift 2
			;;
		--pi-settings)
			[[ $# -eq 2 ]] || usage
			is_folder "${2%/}" || { echo "'$2' is not an absolute folder such as /home/<you>/data-den." >&2; exit 2; }
			pi_settings "${2%/}"
			exit
			;;
		*) usage ;;
	esac
done
if [[ -n "$pi" ]] && ! is_ipv4 "$pi"; then
	echo "'$pi' is not an IPv4 address." >&2
	exit 2
fi
if [[ -n "$dir" ]] && ! is_folder "$dir"; then
	echo "'$dir' is not an absolute folder such as /home/<you>/data-den." >&2
	exit 2
fi
[[ $EUID -eq 0 ]] || { echo "Run as root (sudo $0)." >&2; exit 1; }
[[ -f "$here/../deploy/compose.yaml" ]] ||
	{ echo "No deploy/compose.yaml next to this folder; copy the deploy folder into the VM too." >&2; exit 1; }

user="${SUDO_USER:-}"
if [[ -z "$dir" ]]; then
	[[ -n "$user" && "$user" != root ]] ||
		{ echo "Run it with sudo from your own account, or give the folder with --dir." >&2; exit 2; }
	dir="$(getent passwd "$user" | cut -d: -f6)/data-den"
fi

if [[ -z "$pi" ]]; then
	read -rp "The Pi's address in the home network (such as 192.168.1.20): " pi
	is_ipv4 "$pi" || { echo "'$pi' is not an IPv4 address." >&2; exit 2; }
fi

step "1/9 Mirror of the data disks"
if [[ -e "$array" ]]; then
	echo "$array is there already."
else
	# Whole disks with nothing mounted: the system disk is left out.
	candidates=()
	while read -r name type; do
		[[ "$type" == disk ]] || continue
		lsblk -no MOUNTPOINTS "$name" | grep -q . && continue
		id=""
		for link in /dev/disk/by-id/*; do
			[[ "$link" == *-part* ]] && continue
			if [[ "$(readlink -f "$link")" == "$name" ]]; then
				id="$link"
				break
			fi
		done
		candidates+=("${id:-$name}")
	done < <(lsblk -dnpo NAME,TYPE)
	[[ ${#candidates[@]} -ge 2 ]] ||
		{ echo "Found ${#candidates[@]} free disks, need two: are both passed through (vm/New-DataDenVM.ps1)?" >&2; exit 1; }
	for i in "${!candidates[@]}"; do
		printf '%d) %s  %s\n' "$((i + 1))" "${candidates[$i]}" "$(lsblk -dno SIZE,MODEL,SERIAL "${candidates[$i]}" | xargs)"
	done
	read -rp "The numbers of the two data disks, such as 1 2: " first second
	for n in "$first" "$second"; do
		[[ "$n" =~ ^[0-9]+$ && $n -ge 1 && $n -le ${#candidates[@]} ]] ||
			{ echo "'$n' is not one of the numbers above." >&2; exit 2; }
	done
	"$here/setup-raid.sh" "${candidates[$((first - 1))]}" "${candidates[$((second - 1))]}"
fi

step "2/9 Firewall: only the Pi ($pi) gets in"
"$here/setup-firewall.sh" "$pi"

step "3/9 Automatic security updates"
if [[ -f /etc/apt/apt.conf.d/52data-den-updates ]]; then
	echo "On already; change them with vm/setup-updates.sh."
else
	"$here/setup-updates.sh" on
fi

step "4/9 Docker"
if docker compose version >/dev/null 2>&1; then
	echo "Docker is there already."
else
	DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose
fi

step "5/9 Login at ghcr.io"
if grep -qs '"ghcr.io"' /root/.docker/config.json; then
	echo "Logged in already."
else
	echo "A token with only the scope read:packages, from GitHub under Settings > Developer settings > Personal access tokens (deploy/README.md, step 2)."
	read -rp "Your GitHub username: " github_user
	[[ "$github_user" =~ ^[A-Za-z0-9-]+$ ]] || { echo "'$github_user' is not a GitHub username." >&2; exit 2; }
	docker login ghcr.io -u "$github_user"
fi

step "6/9 Folder $dir"
install -d -m 755 "$dir" "$dir/tls"
if [[ ! -f "$dir/compose.yaml" ]]; then
	install -m 644 "$here/../deploy/compose.yaml" "$dir/compose.yaml"
elif ! cmp -s "$here/../deploy/compose.yaml" "$dir/compose.yaml"; then
	echo "Kept your $dir/compose.yaml, which differs from deploy/compose.yaml."
fi
if [[ ! -f "$dir/.env" ]]; then
	install -m 600 /dev/null "$dir/.env"
fi
if ! grep -q '^DATA_DEN_TRUSTED_PROXY=' "$dir/.env"; then
	echo "DATA_DEN_TRUSTED_PROXY=$pi" >> "$dir/.env"
fi
if [[ -n "$user" && "$user" != root ]]; then
	chown "$user:" "$dir" "$dir/tls" "$dir/compose.yaml" "$dir/.env"
fi

step "7/9 Certificate for the way from the Pi"
if [[ -f "$dir/tls/cert.pem" && -f "$dir/tls/key.pem" ]]; then
	echo "There already."
else
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
		-days 3650 -subj "/CN=data-den" -addext "subjectAltName=DNS:data-den" \
		-keyout "$dir/tls/key.pem" -out "$dir/tls/cert.pem" 2>/dev/null
	chown "$image_user" "$dir/tls/key.pem"
	chmod 400 "$dir/tls/key.pem"
fi

step "8/9 Start DATA-DEN"
mountpoint -q "$data" || { echo "$data is not mounted; open it with sudo vm/disk-key.sh unlock." >&2; exit 1; }
if [[ "$(stat -c %u "$data")" != "$image_user" ]]; then
	chown -R "$image_user:$image_user" "$data"
fi
(cd "$dir" && docker compose pull && docker compose up -d)
healthy=false
for _ in $(seq 30); do
	if curl -ksf "https://localhost:$port/api/health" >/dev/null; then
		healthy=true
		break
	fi
	sleep 2
done
$healthy || { echo "DATA-DEN does not answer; see: cd $dir && sudo docker compose logs" >&2; exit 1; }
echo "DATA-DEN answers on port $port."

step "9/9 Deploys from Windows"
"$here/setup-deploy.sh" on "$dir"

step "Done. On the Pi, in config/rpi-setup.env (rpi-setup task gateway):"
pi_settings "$dir"
echo "GATEWAY_DOMAIN=<the Pi's public name, such as nas.example.org>"
code="$data/.data-den-accounts/setup-code"
if [[ -f "$code" ]]; then
	echo
	echo "Then open DATA-DEN through the Pi and create the owner's account with the setup code:"
	echo "  $(cat "$code")"
fi
