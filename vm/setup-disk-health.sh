#!/usr/bin/env bash
# Switches the disk health check on or off: every 15 minutes the VM reads
# what Windows found on the data disks (Watch-DataDenDisks.ps1 on the PC)
# and DATA-DEN notifies the admins when a disk turns bad. Run in the VM as
# root:
#
#   sudo vm/setup-disk-health.sh on
#   sudo vm/setup-disk-health.sh off
#   sudo vm/setup-disk-health.sh status
#
# Guide: vm/README.md#disk-health
set -euo pipefail

here="$(dirname "$0")"

usage() {
	echo "Usage: $0 on | off | status" >&2
	exit 2
}

need_root() {
	[[ $EUID -eq 0 ]] || { echo "Run as root (sudo $0 $1)." >&2; exit 1; }
}

[[ $# -eq 1 ]] || usage
case "$1" in
	on)
		need_root on
		# hv_kvp_daemon writes what Windows hands over into /var/lib/hyperv.
		if ! command -v hv_kvp_daemon >/dev/null; then
			DEBIAN_FRONTEND=noninteractive apt-get install -y hyperv-daemons
		fi
		systemctl enable --now hv-kvp-daemon.service
		install -m 755 "$here/disk-health.sh" /usr/local/sbin/data-den-disk-health
		install -m 755 "$here/event.sh" /usr/local/sbin/data-den-event
		install -m 644 "$here/data-den-disk-health.service" "$here/data-den-disk-health.timer" /etc/systemd/system/
		systemctl daemon-reload
		systemctl enable --now data-den-disk-health.timer
		echo "The disk health check is on. On the PC, as administrator: vm\\Watch-DataDenDisks.ps1 -Install"
		;;
	off)
		need_root off
		if [[ -f /etc/systemd/system/data-den-disk-health.timer ]]; then
			systemctl disable --now data-den-disk-health.timer
		fi
		echo "The disk health check is off."
		;;
	status)
		need_root status
		if ! systemctl is-enabled --quiet data-den-disk-health.timer 2>/dev/null; then
			echo "The disk health check is off."
			exit 0
		fi
		echo "The disk health check is on. Last reported:"
		cat /var/lib/data-den/disk-health 2>/dev/null || echo "(nothing yet)"
		systemctl list-timers --no-pager data-den-disk-health.timer
		;;
	*) usage ;;
esac
