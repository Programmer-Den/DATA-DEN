#!/usr/bin/env bash
# Lets only the Pi into the VM (nftables), as a second wall behind the
# Hyper-V port ACLs. Run in the VM as root, and again when the Pi's address
# changes:
#
#   sudo vm/setup-firewall.sh 192.168.1.20
#   vm/setup-firewall.sh --print 192.168.1.20   (only shows the rules)
#
# Guide: vm/README.md
set -euo pipefail

usage() {
	echo "Usage: $0 [--print] PI_ADDRESS   (the Pi's IPv4 address)" >&2
	exit 2
}

print=false
if [[ "${1:-}" == "--print" ]]; then
	print=true
	shift
fi
[[ $# -eq 1 ]] || usage
pi="$1"

# Four numbers from 0 to 255, so nothing else ends up in the rules.
octet='(25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])'
if [[ ! "$pi" =~ ^$octet\.$octet\.$octet\.$octet$ ]]; then
	echo "'$pi' is not an IPv4 address." >&2
	exit 2
fi

rules="$(sed "s/@PI_ADDRESS@/$pi/g" "$(dirname "$0")/nftables.conf")"
if $print; then
	echo "$rules"
	exit 0
fi
[[ $EUID -eq 0 ]] || { echo "Run as root (sudo $0 $pi)." >&2; exit 1; }

if ! command -v nft >/dev/null; then
	DEBIAN_FRONTEND=noninteractive apt-get install -y nftables
fi
# Checked before it replaces the running rules.
nft -c -f - <<< "$rules"
echo "$rules" > /etc/nftables.conf
systemctl enable --now nftables
nft -f /etc/nftables.conf
echo "Only $pi gets in now (SSH and HTTPS)."
