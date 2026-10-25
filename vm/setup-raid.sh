#!/usr/bin/env bash
# Mirrors the two passed-through data disks (RAID 1 with mdadm) and mounts
# the mirror at /srv/data-den. ERASES both disks. Run once in the VM as root:
#
#   sudo vm/setup-raid.sh /dev/disk/by-id/<disk 1> /dev/disk/by-id/<disk 2>
#
# Guide: vm/README.md
set -euo pipefail

array=/dev/md/data-den
mountpoint=/srv/data-den

usage() {
	echo "Usage: $0 [--yes] DISK1 DISK2   (whole disks, such as /dev/disk/by-id/scsi-...)" >&2
	exit 2
}

yes=false
disks=()
for arg in "$@"; do
	case "$arg" in
		--yes) yes=true ;;
		-*) usage ;;
		*) disks+=("$arg") ;;
	esac
done
[[ ${#disks[@]} -eq 2 ]] || usage

for disk in "${disks[@]}"; do
	[[ -b "$disk" ]] || { echo "$disk is not a block device." >&2; exit 2; }
	# Whole disks only: a partition has a parent disk.
	if [[ -n "$(lsblk -dno PKNAME "$disk")" ]]; then
		echo "$disk is a partition; give the whole disk." >&2
		exit 2
	fi
done
if [[ "$(readlink -f "${disks[0]}")" == "$(readlink -f "${disks[1]}")" ]]; then
	echo "Both arguments are the same disk." >&2
	exit 2
fi
[[ $EUID -eq 0 ]] || { echo "Run as root (sudo $0 ...)." >&2; exit 1; }

if [[ -e "$array" ]] || grep -qs 'data-den' /etc/mdadm/mdadm.conf; then
	echo "$array already exists; nothing to do." >&2
	exit 1
fi
for disk in "${disks[@]}"; do
	if lsblk -no MOUNTPOINTS "$disk" | grep -q .; then
		echo "$disk or one of its partitions is mounted; is it the system disk?" >&2
		exit 1
	fi
done

lsblk -o NAME,SIZE,MODEL,SERIAL,FSTYPE "${disks[@]}"
if ! $yes; then
	read -rp "Both disks above are erased. Type DATA-DEN to go on: " answer
	[[ "$answer" == "DATA-DEN" ]] || { echo "Stopped, nothing changed." >&2; exit 1; }
fi

if ! command -v mdadm >/dev/null; then
	DEBIAN_FRONTEND=noninteractive apt-get install -y mdadm
fi

wipefs --all "${disks[@]}"
# The internal bitmap keeps the resync after a crash short. The first resync
# of 4 TB takes hours; the mirror can be used meanwhile.
mdadm --create "$array" --run --level=1 --raid-devices=2 --metadata=1.2 \
	--bitmap=internal --name=data-den "${disks[@]}"
mkfs.ext4 -L data-den -m 1 "$array"

# The array assembles at boot from mdadm.conf, and mdadm's monitor mails root
# when a disk fails.
mdadm --detail --scan "$array" >> /etc/mdadm/mdadm.conf
update-initramfs -u

mkdir -p "$mountpoint"
uuid="$(blkid -s UUID -o value "$array")"
# nofail: the VM still boots, without the data, if the mirror is missing.
echo "UUID=$uuid $mountpoint ext4 defaults,noatime,nofail 0 2" >> /etc/fstab
systemctl daemon-reload
mount "$mountpoint"

echo "RAID 1 is up and mounted at $mountpoint. Resync progress: cat /proc/mdstat"
