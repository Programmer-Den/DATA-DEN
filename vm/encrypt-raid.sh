#!/usr/bin/env bash
# Encrypts a mirror that setup-raid.sh made before it encrypted, without
# losing data. Run in the VM as root, with DATA-DEN stopped:
#
#   sudo vm/encrypt-raid.sh start    # moves the data onto one disk, encrypted
#   sudo vm/encrypt-raid.sh finish   # erases the other disk and mirrors onto it
#
# `start` takes one disk out of the mirror, encrypts it, copies everything
# over, compares the copy and switches /srv/data-den to it. The other disk
# keeps the old, unencrypted data untouched until `finish`, so there are two
# copies at every step. Guide: vm/README.md#encrypting-an-existing-mirror
set -Eeuo pipefail

# shellcheck source=vm/luks.sh
source "$(dirname "$0")/luks.sh"

array=/dev/md/data-den
new_array=/dev/md/data-den-new
old_array=/dev/md/data-den-old
mountpoint=/srv/data-den
copy=/mnt/data-den-new
state=/etc/data-den/encrypt-raid.state

usage() {
	echo "Usage: $0 start|finish [--yes]" >&2
	exit 2
}

[[ $# -ge 1 && $# -le 2 ]] || usage
command="$1"
yes=false
case "${2-}" in
	'') ;;
	--yes) yes=true ;;
	*) usage ;;
esac
case "$command" in
	start | finish) ;;
	*) usage ;;
esac
[[ $EUID -eq 0 ]] || { echo "Run as root (sudo $0 $command)." >&2; exit 1; }

confirm() {
	$yes && return
	local answer
	read -rp "$1 Type DATA-DEN to go on: " answer
	[[ "$answer" == "DATA-DEN" ]] || { echo "Stopped, nothing changed." >&2; exit 1; }
}

# md_name DISK: the array name in DISK's mdadm superblock, without the host.
md_name() {
	mdadm --examine --export "$1" 2>/dev/null | sed -n 's/^MD_NAME=\(.*:\)\?//p'
}

# rename_array DEVICE NAME DISK: assembles the array on DISK under NAME.
rename_array() {
	mdadm --assemble "$1" --run --update=name --name="$2" "$3"
}

step=''
on_error() {
	cat >&2 <<-EOF

		Stopped while: $step
		Nothing was erased: the unencrypted data is still on $keep.
		How to go back: vm/README.md#if-the-encryption-stops
	EOF
}

start() {
	[[ ! -e "$state" ]] || { echo "Already started; run: $0 finish" >&2; exit 1; }
	[[ -e "$array" ]] || { echo "There is no mirror $array; set it up with setup-raid.sh." >&2; exit 1; }
	if [[ "$(blkid -s TYPE -o value "$array")" == crypto_LUKS ]]; then
		echo "The mirror is already encrypted." >&2
		exit 1
	fi
	mountpoint -q "$mountpoint" || { echo "$mountpoint is not mounted; mount it first." >&2; exit 1; }

	local md degraded sync_action
	md="$(basename "$(readlink -f "$array")")"
	degraded="$(cat "/sys/block/$md/md/degraded")"
	sync_action="$(cat "/sys/block/$md/md/sync_action")"
	if [[ "$degraded" != 0 || "$sync_action" != idle ]]; then
		echo "The mirror is not complete or still syncing (cat /proc/mdstat); wait until it shows [UU]." >&2
		exit 1
	fi

	# Both members; the last one is encrypted first, the first keeps the data.
	local members
	mapfile -t members < <(mdadm --detail --export "$array" | sed -n 's/^MD_DEVICE_dev_.*_DEV=//p' | sort)
	[[ ${#members[@]} -eq 2 ]] || { echo "Expected 2 disks in the mirror, found ${#members[@]}." >&2; exit 1; }
	keep="${members[0]}"
	local take="${members[1]}"

	# The copy needs the space in use plus the LUKS header (16 MiB).
	local used size
	used="$(df --output=used -B1 "$mountpoint" | tail -n 1)"
	size="$(cat "/sys/block/$md/md/component_size")" # KiB
	if (( used > (size - 32 * 1024) * 1024 * 95 / 100 )); then
		echo "The mirror is too full for the copy; free some space first." >&2
		exit 1
	fi

	luks_install_tools
	luks_require_tpm
	command -v rsync >/dev/null || DEBIAN_FRONTEND=noninteractive apt-get install -y rsync

	lsblk -o NAME,SIZE,MODEL,SERIAL "$keep" "$take"
	cat <<-EOF

		$take leaves the mirror, is ERASED and gets the encrypted copy.
		$keep keeps the data as it is until "$0 finish".
		Stop DATA-DEN first (docker compose stop); the copy of all data takes hours.

	EOF
	confirm "Go on?"
	trap on_error ERR

	step="making $mountpoint read-only (is DATA-DEN stopped?)"
	mount -o remount,ro "$mountpoint"

	step="taking $take out of the mirror"
	mdadm --manage "$array" --fail "$take"
	mdadm --manage "$array" --remove "$take"
	mdadm --zero-superblock "$take"
	wipefs --all "$take"

	step="encrypting $take"
	# As big as the old mirror, so the other disk fits in later.
	mdadm --create "$new_array" --run --level=1 --raid-devices=2 --metadata=1.2 \
		--bitmap=internal --name=data-den-new --size="$size" "$take" missing
	luks_format "$new_array" "$yes"
	mkfs.ext4 -L data-den -m 1 "$mapper_dev"
	mkdir -p "$copy"
	mount "$mapper_dev" "$copy"

	step="copying the data"
	rsync -aHAX --numeric-ids --exclude=/lost+found --info=progress2 "$mountpoint/" "$copy/"

	step="comparing the copy with the original"
	echo "Comparing every file with the original; this takes about as long as the copy."
	local differences
	differences="$(rsync -aHAXn --numeric-ids --checksum --delete --exclude=/lost+found --itemize-changes "$mountpoint/" "$copy/")"
	if [[ -n "$differences" ]]; then
		printf 'The copy differs from the original:\n%s\n' "$differences" >&2
		false
	fi

	step="switching $mountpoint to the encrypted copy"
	umount "$copy"
	rmdir "$copy"
	cryptsetup close "$mapper"
	umount "$mountpoint"
	mdadm --stop "$array"
	mdadm --stop "$new_array"
	# The old disk's array becomes data-den-old, the new one data-den.
	rename_array "$old_array" data-den-old "$keep"
	mdadm --stop "$old_array"
	rename_array "$array" data-den "$take"

	mkdir -p "$(dirname "$state")"
	echo "$keep" > "$state"
	sed -i '/^ARRAY \/dev\/md\/data-den[[:space:]]/d' /etc/mdadm/mdadm.conf
	mdadm --detail --scan "$array" >> /etc/mdadm/mdadm.conf
	update-initramfs -u
	luks_guard_mountpoint "$mountpoint"
	luks_write_config "$array" "$mountpoint"
	trap - ERR

	# Unlocks with the TPM, as at boot, to show that it works.
	if ! systemctl start "systemd-cryptsetup@$(systemd-escape "$mapper").service"; then
		echo "The TPM did not unlock the mirror; open it with: sudo vm/disk-key.sh unlock" >&2
		exit 1
	fi
	mount "$mountpoint"

	cat <<-EOF

		$mountpoint is now encrypted, on $take alone. $keep still has the old,
		unencrypted data (array data-den-old, stopped).
		Start DATA-DEN (docker compose start) and check your files. Then:
		  sudo $0 finish
	EOF
}

finish() {
	[[ -s "$state" ]] || { echo "Nothing to finish; run $0 start first." >&2; exit 1; }
	local old
	old="$(cat "$state")"
	[[ -b "$old" ]] || { echo "$old from $state is not a disk any more." >&2; exit 1; }
	if [[ "$(md_name "$old")" != data-den-old ]]; then
		echo "$old is not the old mirror's disk (no data-den-old on it); nothing changed." >&2
		exit 1
	fi
	if [[ "$(blkid -s TYPE -o value "$array")" != crypto_LUKS ]] || ! mountpoint -q "$mountpoint"; then
		echo "The encrypted mirror is not mounted at $mountpoint; run: sudo vm/disk-key.sh unlock" >&2
		exit 1
	fi

	lsblk -o NAME,SIZE,MODEL,SERIAL "$old"
	confirm "$old with the old, unencrypted data is ERASED and joins the encrypted mirror."

	# The array may have been assembled at boot.
	if [[ -e "$old_array" ]]; then
		mdadm --stop "$old_array"
	fi
	mdadm --zero-superblock "$old"
	wipefs --all "$old"
	# The resync overwrites the whole disk, the old data included.
	mdadm --manage "$array" --add "$old"
	rm -f "$state"

	echo "The mirror is complete again and encrypted. Resync progress: cat /proc/mdstat"
}

keep=''
"$command"
