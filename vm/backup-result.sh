#!/usr/bin/env bash
# Runs after each backup (ExecStopPost in data-den-backup.service): when the
# run failed, DATA-DEN tells the admins, with the end of the run's log.
# Installed as /usr/local/sbin/data-den-backup-result by setup-backup.sh.
#
# Guide: vm/README.md#backup-optional
set -euo pipefail

# systemd sets SERVICE_RESULT ("success", "exit-code", "timeout", ...) and
# EXIT_STATUS for ExecStopPost.
result="${SERVICE_RESULT:-}"
if [[ -z "$result" ]]; then
	echo "SERVICE_RESULT is not set; this runs as ExecStopPost of data-den-backup.service." >&2
	exit 2
fi
[[ "$result" =~ ^[a-z-]+$ ]] || result=unknown

# keep_state: the last run, for den-watcher (DATA_DEN_MONITOR_ADDR): how it
# went, when it ended and when the last successful one did.
keep_state() {
	local state_file now success
	state_file="${DATA_DEN_STATE_DIR:-/var/lib/data-den}/backup-result"
	now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	success="$(sed -n 's/^success=//p' "$state_file" 2>/dev/null | head -n 1)"
	[[ "$result" == success ]] && success="$now"
	mkdir -p "$(dirname "$state_file")" &&
		printf 'result=%s\nfinished=%s\n%s' "$result" "$now" "${success:+success=$success
}" > "$state_file.tmp" &&
		mv -f "$state_file.tmp" "$state_file"
}
# A failed run is still reported when this does not work.
keep_state || echo "Could not keep the result for den-watcher." >&2

[[ "$result" == success ]] && exit 0

log=""
if [[ -n "${INVOCATION_ID:-}" ]]; then
	log="$(journalctl --no-pager -o cat -n 15 _SYSTEMD_INVOCATION_ID="$INVOCATION_ID" 2>/dev/null || true)"
fi
body="Das nächtliche Backup ist fehlgeschlagen ($result, Status ${EXIT_STATUS:-?}). Die letzte Sicherung bleibt erhalten, aber neue Dateien sind nicht gesichert.

${log:-Kein Protokoll gefunden.}

Mehr: sudo journalctl -u data-den-backup"
exec "${DATA_DEN_EVENT:-/usr/local/sbin/data-den-event}" backup "Backup fehlgeschlagen" "$body"
