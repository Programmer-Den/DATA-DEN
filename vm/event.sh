#!/usr/bin/env bash
# Hands an event to DATA-DEN, which sends it to the admins by email or push
# (README.md#notifications): a JSON file in .data-den-events/ on the mirror.
# Installed as /usr/local/sbin/data-den-event by setup-backup.sh and
# setup-smart.sh:
#
#   data-den-event backup "Backup fehlgeschlagen" "What went wrong"
#   data-den-event disk "Platte sdb: SMART-Warnung" "What smartd saw"
#
# DATA_DEN_EVENTS_DIR changes the folder, for tests.
set -euo pipefail

source_dir=/srv/data-den
events_dir="${DATA_DEN_EVENTS_DIR:-$source_dir/.data-den-events}"
# The image's user, which must be able to delete the files once sent.
server_user=65532

if [[ $# -ne 3 ]]; then
	echo "Usage: $0 backup|disk TITLE BODY" >&2
	exit 2
fi
kind="$1" title="$2" body="$3"
if [[ "$kind" != backup && "$kind" != disk ]]; then
	echo "Unknown kind '$kind': backup or disk." >&2
	exit 2
fi
if [[ -z "$title" ]]; then
	echo "The title is empty." >&2
	exit 2
fi

# json STRING: STRING as a JSON string. Control characters other than line
# breaks and tabs are dropped.
json() {
	local s="$1"
	s="${s//\\/\\\\}"
	s="${s//\"/\\\"}"
	s="${s//$'\r'/}"
	s="${s//$'\n'/\\n}"
	s="${s//$'\t'/\\t}"
	s="$(LC_ALL=C tr -d '\000-\010\013\014\016-\037' <<< "$s")"
	printf '"%s"' "$s"
}

if [[ -z "${DATA_DEN_EVENTS_DIR:-}" ]]; then
	# Without the mirror the folder would land on the system disk, where
	# DATA-DEN never looks.
	if ! mountpoint -q "$source_dir"; then
		echo "$source_dir is not mounted; the event is only in the journal: $title: $body" >&2
		exit 1
	fi
	install -d -m 755 -o "$server_user" -g "$server_user" "$events_dir"
fi

# Written under another name first, so DATA-DEN never reads half a file.
# Cut to 8000 bytes; DATA-DEN reads no file above 16 KB.
name="$(date -u +%Y%m%dT%H%M%S)-$$-$kind"
tmp="$events_dir/.$name.tmp"
printf '{"kind": %s, "title": %s, "body": %s}\n' "$(json "$kind")" "$(json "${title:0:200}")" "$(json "${body:0:8000}")" > "$tmp"
chmod 644 "$tmp"
mv "$tmp" "$events_dir/$name.json"
