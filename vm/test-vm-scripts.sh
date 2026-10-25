#!/usr/bin/env bash
# Tests what the VM scripts check before they change anything. Needs neither
# root nor real disks, so CI runs it: vm/test-vm-scripts.sh
set -euo pipefail

cd "$(dirname "$0")"
failed=0

# expect CODE PATTERN COMMAND...: the command exits with CODE and its output
# contains PATTERN.
expect() {
	local code="$1" pattern="$2" out status=0
	shift 2
	out="$("$@" 2>&1)" || status=$?
	if [[ $status -ne $code ]] || ! grep -qF -- "$pattern" <<< "$out"; then
		printf '[FAIL] %s: exit %s, wanted %s with "%s"\n%s\n' "$*" "$status" "$code" "$pattern" "$out" >&2
		failed=1
	else
		printf '[PASS] %s\n' "$*"
	fi
}

expect 2 'Usage' ./setup-raid.sh
expect 2 'Usage' ./setup-raid.sh /dev/sdb
expect 2 'Usage' ./setup-raid.sh /dev/sdb /dev/sdc /dev/sdd
expect 2 'Usage' ./setup-raid.sh --force /dev/sdb /dev/sdc
expect 2 'not a block device' ./setup-raid.sh /nonexistent-1 /nonexistent-2
expect 2 'not a block device' ./setup-raid.sh /dev/null /dev/null

expect 2 'Usage' ./setup-firewall.sh
expect 2 'Usage' ./setup-firewall.sh 192.168.1.20 10.0.0.1
for bad in 256.1.1.1 192.168.1 192.168.1.020x '192.168.1.20 tcp dport 1-65535' 'x/g;s/y' ''; do
	expect 2 'not an IPv4 address' ./setup-firewall.sh "$bad"
done
expect 0 'ip saddr 192.168.1.20 tcp dport { 22, 8443 } accept' ./setup-firewall.sh --print 192.168.1.20

if ./setup-firewall.sh --print 10.0.0.5 | grep -q '@PI_ADDRESS@'; then
	echo '[FAIL] setup-firewall.sh --print left @PI_ADDRESS@ in the rules' >&2
	failed=1
fi

exit "$failed"
