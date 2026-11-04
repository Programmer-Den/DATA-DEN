#!/usr/bin/env bash
# Switches the optional backup of the mirror on or off. It is off until you
# switch it on. With it on, restic takes an encrypted, versioned snapshot of
# /srv/data-den every night, onto an external disk or offsite. Run in the VM
# as root:
#
#   sudo vm/setup-backup.sh on /mnt/backup/data-den        (external disk)
#   sudo vm/setup-backup.sh on sftp:user@host:data-den      (offsite)
#   sudo vm/setup-backup.sh off
#   sudo vm/setup-backup.sh status
#   vm/setup-backup.sh on --print REPOSITORY   (only shows the settings)
#
# Guide: vm/README.md#backup-optional
set -euo pipefail

here="$(dirname "$0")"
config_dir=/etc/data-den
env_file="$config_dir/backup.env"
password_file="$config_dir/backup-password"
source_dir=/srv/data-den

usage() {
	cat >&2 <<USAGE
Usage: $0 on [--print] [--keep-daily N] [--keep-weekly N] [--keep-monthly N] REPOSITORY
       $0 off | status
REPOSITORY: a folder on an external disk (/mnt/backup/data-den) or a restic
repository elsewhere (sftp:user@host:data-den, rest:..., s3:..., b2:..., rclone:...)
USAGE
	exit 2
}

need_root() {
	[[ $EUID -eq 0 ]] || { echo "Run as root (sudo $0 $*)." >&2; exit 1; }
}

# restic with the settings from the env files, as the timer runs it.
run_restic() {
	(
		set -a
		# shellcheck source=/dev/null
		source "$env_file"
		# shellcheck source=/dev/null
		[[ -f "$config_dir/backup-credentials.env" ]] && source "$config_dir/backup-credentials.env"
		set +a
		export RESTIC_CACHE_DIR=/var/cache/data-den-backup
		restic "$@"
	)
}

switch_on() {
	local print=false daily=7 weekly=4 monthly=12 repo=""
	while [[ $# -gt 0 ]]; do
		case "$1" in
			--print) print=true ;;
			--keep-daily | --keep-weekly | --keep-monthly)
				[[ $# -ge 2 && "$2" =~ ^[1-9][0-9]{0,3}$ ]] || { echo "$1 needs a number from 1 to 9999." >&2; exit 2; }
				case "$1" in
					--keep-daily) daily="$2" ;;
					--keep-weekly) weekly="$2" ;;
					--keep-monthly) monthly="$2" ;;
				esac
				shift
				;;
			-*) usage ;;
			*) [[ -z "$repo" ]] || usage; repo="$1" ;;
		esac
		shift
	done
	[[ -n "$repo" ]] || usage

	# Only characters that are safe in the env file and in restic's
	# repository syntax: no spaces, quotes, $ or line breaks.
	if [[ ! "$repo" =~ ^(/[A-Za-z0-9._~/-]+|(sftp|rest|s3|b2|azure|gs|swift|rclone):[A-Za-z0-9._~/@:%+=-]+)$ ]]; then
		echo "'$repo' is not a repository: give an absolute folder or sftp:, rest:, s3:, b2:, azure:, gs:, swift: or rclone:." >&2
		exit 2
	fi
	if [[ "$repo" == /* ]]; then
		if [[ "/$repo/" == *"/../"* || "/$repo/" == *"/./"* ]]; then
			echo "'$repo' has . or .. in it; give the plain path." >&2
			exit 2
		fi
		repo="${repo%/}"
		if [[ -z "$repo" || "$repo/" == "$source_dir/"* ]]; then
			echo "'$repo' is on the mirror itself; a backup there is lost with it." >&2
			exit 2
		fi
	fi

	local settings
	settings="# DATA-DEN backup, written by vm/setup-backup.sh
RESTIC_REPOSITORY=$repo
RESTIC_PASSWORD_FILE=$password_file
BACKUP_KEEP_DAILY=$daily
BACKUP_KEEP_WEEKLY=$weekly
BACKUP_KEEP_MONTHLY=$monthly"
	if $print; then
		echo "$settings"
		exit 0
	fi
	need_root on "$repo"

	if [[ "$repo" == /* ]]; then
		local disk
		disk="$(findmnt -no TARGET -T "$repo" 2>/dev/null || true)"
		if [[ ! -d "$repo" || -z "$disk" || "$disk" == / ]]; then
			echo "$repo must be a folder on a mounted backup disk, not on the VM's system disk. Mount the disk and create the folder first." >&2
			exit 1
		fi
	fi
	if ! command -v restic >/dev/null; then
		DEBIAN_FRONTEND=noninteractive apt-get install -y restic
	fi

	install -d -m 700 "$config_dir"
	local new_password=false
	if [[ ! -s "$password_file" ]]; then
		(umask 077 && head -c 32 /dev/urandom | base64 > "$password_file")
		new_password=true
	fi
	(umask 077 && echo "$settings" > "$env_file")

	# A repository that already exists is kept, with its snapshots.
	if ! run_restic cat config >/dev/null 2>&1; then
		run_restic init
	fi

	install -m 755 "$here/backup.sh" /usr/local/sbin/data-den-backup
	install -m 755 "$here/backup-result.sh" /usr/local/sbin/data-den-backup-result
	install -m 755 "$here/event.sh" /usr/local/sbin/data-den-event
	install -m 644 "$here/data-den-backup.service" "$here/data-den-backup.timer" /etc/systemd/system/
	systemctl daemon-reload
	systemctl enable --now data-den-backup.timer

	echo "The backup is on: every night at about 3:00 into $repo,"
	echo "keeping $daily daily, $weekly weekly and $monthly monthly snapshots."
	echo "The first backup now: sudo systemctl start data-den-backup"
	if $new_password; then
		echo
		echo "The repository's password; without it the backup cannot be read."
		echo "Keep it outside the VM, such as in a password manager:"
		echo
		cat "$password_file"
	fi
}

switch_off() {
	[[ $# -eq 0 ]] || usage
	need_root off
	if [[ -f /etc/systemd/system/data-den-backup.timer ]]; then
		systemctl disable --now data-den-backup.timer
	fi
	echo "The backup is off. The settings in $config_dir and the snapshots in the repository are kept;"
	echo "switch it on again with the same repository to go on with them."
}

show_status() {
	[[ $# -eq 0 ]] || usage
	need_root status
	if ! systemctl is-enabled --quiet data-den-backup.timer 2>/dev/null; then
		echo "The backup is off."
		return
	fi
	echo "The backup is on."
	systemctl list-timers --no-pager data-den-backup.timer
	systemctl show --no-pager -p Result -p ExecMainExitTimestamp data-den-backup.service
	run_restic snapshots --tag data-den --latest 5
}

[[ $# -ge 1 ]] || usage
command="$1"
shift
case "$command" in
	on) switch_on "$@" ;;
	off) switch_off "$@" ;;
	status) show_status "$@" ;;
	*) usage ;;
esac
