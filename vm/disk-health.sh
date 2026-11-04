#!/usr/bin/env bash
# Reads the health of the data disks that Windows hands over
# (Watch-DataDenDisks.ps1, Hyper-V key-value exchange) and, when it changes,
# hands an event to DATA-DEN, which notifies the admins: a disk turning bad,
# all disks fine again, or Windows no longer checking. Installed as
# /usr/local/sbin/data-den-disk-health by setup-disk-health.sh; a timer runs
# it every 15 minutes.
#
# Guide: vm/README.md#disk-health
set -euo pipefail

pool="${DATA_DEN_KVP_POOL:-/var/lib/hyperv/.kvp_pool_0}"
state_file="${DATA_DEN_STATE_DIR:-/var/lib/data-den}/disk-health"
event="${DATA_DEN_EVENT:-/usr/local/sbin/data-den-event}"
key=DataDenDiskHealth
# hv_kvp_daemon's records: a 512-byte key and a 2048-byte value, both padded
# with zero bytes.
key_size=512
record_size=2560
# Windows checks every 15 minutes; this long without a check is reported.
stale_seconds=$((24 * 3600))

# value: the value of $key in the pool, empty when there is none.
value() {
	[[ -r "$pool" ]] || return 0
	local size records i
	size="$(stat -c %s "$pool")"
	records=$((size / record_size))
	for ((i = 0; i < records; i++)); do
		if [[ "$(dd if="$pool" bs="$record_size" skip="$i" count=1 status=none | head -c "$key_size" | tr -d '\0')" == "$key" ]]; then
			dd if="$pool" bs="$record_size" skip="$i" count=1 status=none | tail -c +"$((key_size + 1))" | tr -d '\0'
			return 0
		fi
	done
}

current="$(value)"
if [[ -z "$current" ]]; then
	echo "Windows has not handed over the disk health yet (run vm/Watch-DataDenDisks.ps1 -Install on the PC)." >&2
	exit 0
fi
status="$(sed -n 's/^status=//p' <<< "$current" | head -n 1)"
checked="$(sed -n 's/^checked=//p' <<< "$current" | head -n 1)"
problems="$(grep -v -e '^status=' -e '^checked=' <<< "$current" || true)"
if [[ "$status" != ok && "$status" != warn ]] || ! checked_at="$(date -d "$checked" +%s 2>/dev/null)"; then
	echo "The disk health from Windows is not understood: $current" >&2
	exit 1
fi

# The state reported last: ok, stale, or warn and the problems.
before="$(cat "$state_file" 2>/dev/null || echo ok)"
if (($(date +%s) - checked_at > stale_seconds)); then
	now=stale
else
	now="$status"
	[[ "$status" == warn ]] && now="warn
$problems"
fi
[[ "$now" == "$before" ]] && exit 0

case "$now" in
	stale)
		"$event" disk "Plattenprüfung meldet sich nicht" "Windows hat die Platten von DATA-DEN zuletzt am $checked (UTC) geprüft. Läuft die Aufgabe \"DATA-DEN disk health\" noch (Aufgabenplanung)? Ohne sie kommt keine Warnung, bevor eine Platte ausfällt."
		;;
	ok)
		if [[ "$before" == warn* ]]; then
			"$event" disk "Platten wieder in Ordnung" "Windows meldet für die Platten von DATA-DEN keine Probleme mehr."
		fi
		;;
	*)
		"$event" disk "Platte meldet Probleme" "Windows meldet Probleme mit einer Platte von DATA-DEN:

$problems

Noch arbeitet das RAID, aber die Platte kann bald ausfallen. Prüfe das Backup und tausche sie (vm/README.md, \"When a disk fails\")."
		;;
esac
mkdir -p "$(dirname "$state_file")"
printf '%s\n' "$now" > "$state_file"
