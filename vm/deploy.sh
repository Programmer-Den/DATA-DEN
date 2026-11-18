#!/usr/bin/env bash
# Puts the DATA-DEN version that Windows hands over into service: the image
# tag comes from Deploy-DataDen.ps1 on the PC over Hyper-V's key-value
# exchange (key DataDenDeploy), without any network into the VM. It pulls
# that image, restarts DATA-DEN with it and waits until it is healthy; when it
# does not get healthy, it goes back to the image that ran before. The result
# goes back to Windows the same way (key DataDenDeployResult).
#
# Installed as /usr/local/sbin/data-den-deploy by setup-deploy.sh; a path unit
# starts it whenever Windows hands something over. --skip-current marks the
# request handed over last as done without deploying it.
#
# Guide: deploy/README.md#deploy-from-windows
set -euo pipefail

pool="${DATA_DEN_KVP_POOL:-/var/lib/hyperv/.kvp_pool_0}"
result_pool="${DATA_DEN_KVP_RESULT_POOL:-/var/lib/hyperv/.kvp_pool_1}"
state_dir="${DATA_DEN_STATE_DIR:-/var/lib/data-den}"
settings="${DATA_DEN_DEPLOY_SETTINGS:-/etc/data-den/deploy.env}"
docker="${DATA_DEN_DOCKER:-docker}"
# How long the new version has to become healthy.
health_seconds="${DATA_DEN_HEALTH_SECONDS:-180}"
key=DataDenDeploy
result_key=DataDenDeployResult
# hv_kvp_daemon's records: a 512-byte key and a 2048-byte value, both padded
# with zero bytes.
key_size=512
value_size=2048
record_size=$((key_size + value_size))

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

# report STATUS MESSAGE: hands the result to Windows, in the guest's pool,
# which Hyper-V reads as the VM's GuestExchangeItems, and keeps it for
# setup-deploy.sh status.
report() {
	local text dir tmp size records i
	text="$(printf 'id=%s\nstatus=%s\ntag=%s\n%s' "$id" "$1" "$tag" "$2" | head -c $((value_size - 1)))"
	printf '%s\n' "$text" > "$state_dir/deploy-result"
	printf '%s\n' "$text"
	dir="$(dirname "$result_pool")"
	tmp="$(mktemp "$dir/.data-den-deploy.XXXXXX")"
	# Every other record stays; the new one replaces an older result.
	if [[ -r "$result_pool" ]]; then
		size="$(stat -c %s "$result_pool")"
		records=$((size / record_size))
		for ((i = 0; i < records; i++)); do
			if [[ "$(dd if="$result_pool" bs="$record_size" skip="$i" count=1 status=none | head -c "$key_size" | tr -d '\0')" != "$result_key" ]]; then
				dd if="$result_pool" bs="$record_size" skip="$i" count=1 status=none >> "$tmp"
			fi
		done
	fi
	{
		printf '%s' "$result_key"
		head -c $((key_size - ${#result_key})) /dev/zero
		printf '%s' "$text"
		head -c $((value_size - $(printf '%s' "$text" | wc -c))) /dev/zero
	} >> "$tmp"
	chmod 644 "$tmp"
	# hv_kvp_daemon opens the file anew each time, so it sees either the old
	# or the new one.
	mv -f "$tmp" "$result_pool"
}

# fail MESSAGE: reports the failure and stops.
fail() {
	report failed "$1"
	exit 1
}

# set_tag TAG: DATA_DEN_TAG in the .env next to the compose files, which
# compose.yaml puts into the image name; an empty TAG removes it. Written in
# place, so the file keeps its owner and permissions.
set_tag() {
	local content
	content="$(grep -v '^DATA_DEN_TAG=' .env || true)"
	{
		[[ -n "$content" ]] && printf '%s\n' "$content"
		[[ -n "$1" ]] && printf 'DATA_DEN_TAG=%s\n' "$1"
		true
	} > .env
}

# healthy: true once DATA-DEN's container reports healthy; false when it does
# not within $health_seconds.
healthy() {
	local waited=0 container status
	while :; do
		container="$("${compose[@]}" ps -q data-den 2>/dev/null || true)"
		status="$([[ -n "$container" ]] && "$docker" inspect --format '{{.State.Health.Status}}' "$container" 2>/dev/null || true)"
		[[ "$status" == healthy ]] && return 0
		((waited >= health_seconds)) && return 1
		sleep 5
		waited=$((waited + 5))
	done
}

if [[ $# -gt 1 || ( $# -eq 1 && "$1" != --skip-current ) ]]; then
	echo "Usage: $0 [--skip-current]" >&2
	exit 2
fi
request="$(value)"
if [[ -z "$request" ]]; then
	echo "Windows has not handed over a version yet (Deploy-DataDen.ps1 on the PC)." >&2
	exit 0
fi
id="$(sed -n 's/^id=//p' <<< "$request" | head -n 1)"
tag="$(sed -n 's/^tag=//p' <<< "$request" | head -n 1)"
if [[ ! "$id" =~ ^[A-Za-z0-9-]{1,64}$ ]]; then
	echo "The deploy request from Windows is not understood: $request" >&2
	exit 1
fi
mkdir -p "$state_dir"
# Each request runs once, even when it fails: the pool changes for other
# reasons too (the disk health), and a failed version would otherwise be
# tried again each time.
[[ "$(cat "$state_dir/deploy-id" 2>/dev/null || true)" == "$id" ]] && exit 0
printf '%s\n' "$id" > "$state_dir/deploy-id"
[[ "${1:-}" == --skip-current ]] && exit 0
echo "Deploying DATA-DEN '$tag' (request $id)"

[[ "$tag" =~ ^[a-z0-9][a-z0-9._-]{0,127}$ ]] ||
	fail "'$tag' is not an image tag such as 1.2.3, latest, main or sha-abc1234."
dir="$(sed -n 's/^DATA_DEN_COMPOSE_DIR=//p' "$settings" 2>/dev/null | tail -n 1)"
[[ -n "$dir" && -f "$dir/compose.yaml" ]] ||
	fail "No compose.yaml in '$dir'; in the VM: sudo vm/setup-deploy.sh on <the folder with compose.yaml>."
cd "$dir"
grep -q 'DATA_DEN_TAG' compose.yaml ||
	fail "$dir/compose.yaml takes no version yet; copy the newest deploy/compose.yaml into the VM."
[[ -f .env ]] || fail "No .env with the settings in $dir (deploy/README.md, step 5)."

# compose.yaml and every optional file next to it that is in use.
compose=("$docker" compose -f compose.yaml)
for file in compose.*.yaml; do
	[[ -f "$file" ]] && compose+=(-f "$file")
done

# What runs now, to go back to when the new version does not get healthy.
old_tag="$(sed -n 's/^DATA_DEN_TAG=//p' .env | tail -n 1)"
old_container="$("${compose[@]}" ps -q data-den 2>/dev/null || true)"
old_image=
old_ref=
if [[ -n "$old_container" ]]; then
	old_image="$("$docker" inspect --format '{{.Image}}' "$old_container" 2>/dev/null || true)"
	old_ref="$("$docker" inspect --format '{{.Config.Image}}' "$old_container" 2>/dev/null || true)"
fi

set_tag "$tag"
if ! out="$("${compose[@]}" pull --quiet data-den 2>&1)"; then
	set_tag "$old_tag"
	fail "Pulling '$tag' failed, DATA-DEN keeps running as before:
$(tail -n 5 <<< "$out")"
fi
if ! out="$("${compose[@]}" up -d 2>&1)" || ! healthy; then
	logs="$("${compose[@]}" logs --tail 10 data-den 2>&1 || true)"
	if [[ -n "$old_image" && -n "$old_ref" ]]; then
		# The old tag may now name the new image (main, latest), so put it
		# back on the image that ran before.
		"$docker" tag "$old_image" "$old_ref"
		set_tag "${old_ref##*:}"
		"${compose[@]}" up -d >/dev/null 2>&1 || true
		fail "'$tag' did not get healthy within $health_seconds seconds, so DATA-DEN runs '${old_ref##*:}' again as before. Its log:
${out}
${logs}"
	fi
	fail "'$tag' did not get healthy within $health_seconds seconds. Its log:
${out}
${logs}"
fi

container="$("${compose[@]}" ps -q data-den)"
version="$("$docker" inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$container" 2>/dev/null || true)"
"$docker" image prune -f >/dev/null 2>&1 || true
report ok "DATA-DEN runs '$tag'${version:+ (version $version)} and is healthy."
