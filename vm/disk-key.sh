#!/usr/bin/env bash
# The keys of the encrypted mirror. Run in the VM as root:
#
#   sudo vm/disk-key.sh status         # locked or open, and which keys there are
#   sudo vm/disk-key.sh unlock         # opens it when the TPM did not at boot
#   sudo vm/disk-key.sh tpm            # binds it to the TPM again
#   sudo vm/disk-key.sh recovery-key   # replaces the recovery key with a new one
#
# Guide: vm/README.md#encryption
set -euo pipefail

# shellcheck source=vm/luks.sh
source "$(dirname "$0")/luks.sh"

array=/dev/md/data-den
mountpoint=/srv/data-den

usage() {
	echo "Usage: $0 status|unlock|tpm|recovery-key" >&2
	exit 2
}

[[ $# -eq 1 ]] || usage
case "$1" in
	status | unlock | tpm | recovery-key) ;;
	*) usage ;;
esac
[[ $EUID -eq 0 ]] || { echo "Run as root (sudo $0 $1)." >&2; exit 1; }

if [[ ! -e "$array" ]] || [[ "$(blkid -s TYPE -o value "$array")" != crypto_LUKS ]]; then
	echo "The mirror $array is not encrypted; see vm/README.md#encryption" >&2
	exit 1
fi

status() {
	if [[ -e "$mapper_dev" ]]; then
		echo "Open as $mapper_dev."
	else
		echo "Locked. Open it with: sudo $0 unlock"
	fi
	if mountpoint -q "$mountpoint"; then
		df -h "$mountpoint"
	else
		echo "$mountpoint is not mounted."
	fi
	echo
	echo "Keys (tpm2: unlocks at boot, recovery: the key you saved):"
	systemd-cryptenroll "$array"
}

# restart_data_den: containers that use the mirror see it only after a restart.
restart_data_den() {
	command -v docker >/dev/null || return 0
	local id
	for id in $(docker ps -q); do
		if docker inspect -f '{{range .Mounts}}{{.Source}} {{end}}' "$id" | grep -qw -- "$mountpoint"; then
			docker restart "$id" >/dev/null
			echo "Restarted container $(docker inspect -f '{{.Name}}' "$id")."
		fi
	done
}

unlock() {
	if [[ ! -e "$mapper_dev" ]]; then
		# First the TPM, as at boot; then the recovery key.
		if ! systemctl start "systemd-cryptsetup@$(systemd-escape "$mapper").service" 2>/dev/null; then
			echo "The TPM did not unlock the mirror. Type the recovery key."
			cryptsetup open "$array" "$mapper"
			echo "Opened. To let it unlock itself at boot again: sudo $0 tpm"
		fi
	fi
	if ! mountpoint -q "$mountpoint"; then
		mount "$mountpoint"
	fi
	echo "$mountpoint is mounted."
	restart_data_den
}

tpm() {
	luks_require_tpm
	echo "Type the recovery key to bind the mirror to this TPM."
	systemd-cryptenroll --wipe-slot=tpm2 --tpm2-device=auto --tpm2-pcrs="$tpm_pcrs" "$array"
	echo "Bound to the TPM; the mirror unlocks itself at the next boot."
}

recovery_slots() {
	systemd-cryptenroll "$array" | awk '$2 == "recovery" { print $1 }'
}

recovery_key() {
	local old slot
	mapfile -t old < <(recovery_slots)
	# Unlocks with the TPM, so this works even when the old key is lost.
	luks_new_recovery_key "$array" '' false
	for slot in "${old[@]}"; do
		systemd-cryptenroll --unlock-tpm2-device=auto --wipe-slot="$slot" "$array"
	done
	echo "The new recovery key is in place; the old one no longer works."
}

case "$1" in
	recovery-key) recovery_key ;;
	*) "$1" ;;
esac
