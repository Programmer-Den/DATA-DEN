#!/usr/bin/env bash
# Runs after every job (ACTIONS_RUNNER_HOOK_JOB_COMPLETED) and deletes the
# checkout, so the next job starts clean. The runner's own folders (_actions,
# _temp, _tool with the Go and Node downloads) stay. A failure here never fails
# the job.
set -uo pipefail

work="${RUNNER_WORKDIR:-/tmp/runner/work}"
for dir in "$work"/*; do
	[[ -e "$dir" ]] || continue
	case "$(basename "$dir")" in
		_*) ;;
		*) rm -rf "$dir" ;;
	esac
done
exit 0
