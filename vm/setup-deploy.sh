#!/usr/bin/env bash
# Switches deploys from Windows on or off: Deploy-DataDen.ps1 on the PC (by
# hand or from a self-hosted runner) hands an image tag to the VM over
# Hyper-V's key-value exchange, and the VM pulls it and restarts DATA-DEN with
# it. Nothing new gets into the VM over the network. Run in the VM as root,
# with the folder that holds compose.yaml and .env:
#
#   sudo vm/setup-deploy.sh on /home/<you>/data-den
#   sudo vm/setup-deploy.sh off
#   sudo vm/setup-deploy.sh status
#
# Guide: deploy/README.md#deploy-from-windows
set -euo pipefail

here="$(dirname "$0")"
settings=/etc/data-den/deploy.env

usage() {
	echo "Usage: $0 on [--print] FOLDER | off | status" >&2
	exit 2
}

need_root() {
	[[ $EUID -eq 0 ]] || { echo "Run as root (sudo $0 $1)." >&2; exit 1; }
}

case "${1:-}" in
	on)
		shift
		print=false
		if [[ "${1:-}" == --print ]]; then
			print=true
			shift
		fi
		[[ $# -eq 1 && "$1" != -* ]] || usage
		dir="${1%/}"
		if [[ ! "$dir" =~ ^/[A-Za-z0-9._/-]+$ ]]; then
			echo "'$1' is not an absolute folder such as /home/<you>/data-den." >&2
			exit 2
		fi
		if [[ "/$dir/" == *"/../"* || "/$dir/" == *"/./"* ]]; then
			echo "'$1' has . or .. in it; give the folder itself." >&2
			exit 2
		fi
		if [[ "$print" == true ]]; then
			printf 'DATA_DEN_COMPOSE_DIR=%s\n' "$dir"
			exit 0
		fi
		need_root on
		[[ -f "$dir/compose.yaml" && -f "$dir/.env" ]] ||
			{ echo "No compose.yaml and .env in $dir (deploy/README.md)." >&2; exit 1; }
		grep -q 'DATA_DEN_TAG' "$dir/compose.yaml" ||
			{ echo "$dir/compose.yaml takes no version yet; copy the newest deploy/compose.yaml there first." >&2; exit 1; }
		# hv_kvp_daemon writes what Windows hands over into /var/lib/hyperv.
		if ! command -v hv_kvp_daemon >/dev/null; then
			DEBIAN_FRONTEND=noninteractive apt-get install -y hyperv-daemons
		fi
		systemctl enable --now hv-kvp-daemon.service
		install -m 755 -d /etc/data-den
		printf 'DATA_DEN_COMPOSE_DIR=%s\n' "$dir" > "$settings"
		install -m 755 "$here/deploy.sh" /usr/local/sbin/data-den-deploy
		install -m 644 "$here/data-den-deploy.path" "$here/data-den-deploy.service" /etc/systemd/system/
		systemctl daemon-reload
		# The request handed over last counts as done, so switching on does
		# not deploy an old version again.
		install -m 755 -d /var/lib/data-den
		/usr/local/sbin/data-den-deploy --skip-current || true
		systemctl enable --now data-den-deploy.path
		echo "Deploys from Windows are on, for $dir. On the PC, as administrator: vm\\Deploy-DataDen.ps1 -Tag main"
		;;
	off)
		[[ $# -eq 1 ]] || usage
		need_root off
		if [[ -f /etc/systemd/system/data-den-deploy.path ]]; then
			systemctl disable --now data-den-deploy.path
		fi
		echo "Deploys from Windows are off."
		;;
	status)
		[[ $# -eq 1 ]] || usage
		need_root status
		if ! systemctl is-enabled --quiet data-den-deploy.path 2>/dev/null; then
			echo "Deploys from Windows are off."
			exit 0
		fi
		echo "Deploys from Windows are on, for $(sed -n 's/^DATA_DEN_COMPOSE_DIR=//p' "$settings"). The last one:"
		cat /var/lib/data-den/deploy-result 2>/dev/null || echo "(none yet)"
		;;
	*) usage ;;
esac
