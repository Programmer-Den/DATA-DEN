#!/usr/bin/env bash
# The backup run: a new restic snapshot of the mirror, then old snapshots are
# thinned out and the repository is checked. setup-backup.sh installs it as
# /usr/local/sbin/data-den-backup, and data-den-backup.timer runs it every
# night with the settings from /etc/data-den/backup.env.
#
# Guide: vm/README.md#backup-optional
set -euo pipefail

source_dir=/srv/data-den

for var in RESTIC_REPOSITORY RESTIC_PASSWORD_FILE BACKUP_KEEP_DAILY BACKUP_KEEP_WEEKLY BACKUP_KEEP_MONTHLY; do
	if [[ -z "${!var:-}" ]]; then
		echo "$var is not set; switch the backup on with: sudo vm/setup-backup.sh on REPOSITORY" >&2
		exit 1
	fi
done

# Without the mirror the folder is empty, and an empty snapshot would become
# the newest one.
if ! mountpoint -q "$source_dir"; then
	echo "$source_dir is not mounted; no backup." >&2
	exit 1
fi
# A local repository must be on its own disk. If that disk is missing, the
# path is a plain folder on the VM's system disk, which would soon be full.
if [[ "$RESTIC_REPOSITORY" == /* ]]; then
	disk="$(findmnt -no TARGET -T "$RESTIC_REPOSITORY" 2>/dev/null || true)"
	if [[ -z "$disk" || "$disk" == / || ! -d "$RESTIC_REPOSITORY" ]]; then
		echo "The backup disk for $RESTIC_REPOSITORY is not mounted; no backup." >&2
		exit 1
	fi
fi

# Unfinished uploads and thumbnails are left out: they are rebuilt anyway.
restic backup --one-file-system --tag data-den \
	--exclude "$source_dir/.data-den-uploads" \
	--exclude "$source_dir/.data-den-thumbs" \
	"$source_dir"
restic forget --tag data-den --prune \
	--keep-daily "$BACKUP_KEEP_DAILY" \
	--keep-weekly "$BACKUP_KEEP_WEEKLY" \
	--keep-monthly "$BACKUP_KEEP_MONTHLY"
restic check
