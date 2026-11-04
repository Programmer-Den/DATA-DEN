#!/usr/bin/env bash
# Switches automatic security updates of the VM on or off. With them on,
# unattended-upgrades installs Debian's security updates every day, Docker
# included (it comes from Debian), and restarts the VM at a quiet time when
# an update needs it. Run in the VM as root:
#
#   sudo vm/setup-updates.sh on                 (restart at 05:00 when needed)
#   sudo vm/setup-updates.sh on --reboot 04:30
#   sudo vm/setup-updates.sh on --no-reboot     (you restart yourself)
#   sudo vm/setup-updates.sh off
#   sudo vm/setup-updates.sh status
#   vm/setup-updates.sh on --print              (only shows the settings)
#
# Guide: vm/README.md#automatic-updates
set -euo pipefail

settings_file=/etc/apt/apt.conf.d/52data-den-updates
periodic_file=/etc/apt/apt.conf.d/20auto-upgrades

usage() {
	cat >&2 <<USAGE
Usage: $0 on [--print] [--reboot HH:MM | --no-reboot]
       $0 off | status
USAGE
	exit 2
}

need_root() {
	[[ $EUID -eq 0 ]] || { echo "Run as root (sudo $0 $*)." >&2; exit 1; }
}

switch_on() {
	local print=false reboot=true time=05:00
	while [[ $# -gt 0 ]]; do
		case "$1" in
			--print) print=true ;;
			--no-reboot) reboot=false ;;
			--reboot)
				[[ $# -ge 2 && "$2" =~ ^([01][0-9]|2[0-3]):[0-5][0-9]$ ]] || { echo "--reboot needs a time such as 05:00." >&2; exit 2; }
				time="$2"
				shift
				;;
			*) usage ;;
		esac
		shift
	done

	local settings
	# Debian's own 50unattended-upgrades also installs point releases; both
	# origin lists are cleared so only the security archive applies, as on
	# the Pi (rpi-setup's base task).
	settings="// DATA-DEN: automatic updates, written by vm/setup-updates.sh
#clear Unattended-Upgrade::Allowed-Origins;
#clear Unattended-Upgrade::Origins-Pattern;
Unattended-Upgrade::Origins-Pattern {
	\"origin=Debian,codename=\${distro_codename},label=Debian-Security\";
	\"origin=Debian,codename=\${distro_codename}-security,label=Debian-Security\";
};
Unattended-Upgrade::Remove-Unused-Kernel-Packages \"true\";
Unattended-Upgrade::Remove-Unused-Dependencies \"true\";
Unattended-Upgrade::Automatic-Reboot \"$reboot\";
Unattended-Upgrade::Automatic-Reboot-Time \"$time\";
Unattended-Upgrade::Mail \"root\";
Unattended-Upgrade::MailReport \"only-on-error\";"
	local periodic='// Written by vm/setup-updates.sh
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";'
	if $print; then
		echo "$settings"
		echo
		echo "$periodic"
		exit 0
	fi
	need_root on

	if ! command -v unattended-upgrade >/dev/null; then
		apt-get update
		DEBIAN_FRONTEND=noninteractive apt-get install -y unattended-upgrades
	fi
	echo "$settings" > "$settings_file"
	echo "$periodic" > "$periodic_file"
	systemctl enable --now apt-daily.timer apt-daily-upgrade.timer
	# Fails here, not at night, when the settings are wrong.
	unattended-upgrade --dry-run >/dev/null

	echo "Automatic security updates are on: every day, about 6:00."
	if $reboot; then
		echo "When an update needs a restart, the VM restarts at $time; DATA-DEN starts again by itself."
	else
		echo "The VM never restarts by itself; $0 status says when it needs one."
	fi
}

switch_off() {
	[[ $# -eq 0 ]] || usage
	need_root off
	rm -f "$settings_file"
	if [[ -f "$periodic_file" ]]; then
		sed -i 's/"1";/"0";/' "$periodic_file"
	fi
	echo "Automatic updates are off. Install updates yourself: sudo apt update && sudo apt upgrade"
}

show_status() {
	[[ $# -eq 0 ]] || usage
	need_root status
	if [[ -f "$settings_file" ]] && grep -q 'Unattended-Upgrade "1"' "$periodic_file" 2>/dev/null; then
		echo "Automatic updates are on."
		grep -E 'Automatic-Reboot(-Time)? ' "$settings_file"
	else
		echo "Automatic updates are off."
	fi
	systemctl list-timers --no-pager apt-daily.timer apt-daily-upgrade.timer
	if [[ -f /var/run/reboot-required ]]; then
		echo "The VM needs a restart for: $(tr '\n' ' ' < /var/run/reboot-required.pkgs 2>/dev/null)"
	fi
	local log=/var/log/unattended-upgrades/unattended-upgrades.log
	if [[ -f "$log" ]]; then
		echo "Last runs ($log):"
		tail -n 10 "$log"
	fi
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
