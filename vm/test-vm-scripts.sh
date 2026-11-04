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

expect 2 'Usage' ./encrypt-raid.sh
expect 2 'Usage' ./encrypt-raid.sh begin
expect 2 'Usage' ./encrypt-raid.sh start --force
expect 2 'Usage' ./encrypt-raid.sh start --yes now
expect 2 'Usage' ./disk-key.sh
expect 2 'Usage' ./disk-key.sh open
expect 2 'Usage' ./disk-key.sh unlock now

# The encryption's settings and the recovery key's shape.
# shellcheck source=vm/luks.sh
source ./luks.sh
expect 0 'data-den UUID=1234 none luks,tpm2-device=auto,headless=true,nofail' luks_crypttab_line 1234
expect 0 '/dev/mapper/data-den /srv/data-den ext4 defaults,noatime,nofail,x-systemd.before=docker.service 0 2' luks_fstab_line /srv/data-den
key=cdtilclg-nguhkdff-gfehjjvc-uektbfci-bnjrcchn-ckhgkcrt-jhhngdni-bidkdfhv
expect 0 '' luks_is_recovery_key "$key"
for bad in "${key%-*}" "$key-cdtilclg" "${key/c/a}" "$key " ''; do
	expect 1 '' luks_is_recovery_key "$bad"
done
expect 0 "$key" luks_show_recovery_key "$key" false <<< cdtilclg
expect 0 'not the first group' luks_show_recovery_key "$key" false <<< $'nguhkdff\ncdtilclg'
expect 1 'shown only now' luks_show_recovery_key "$key" false < /dev/null
expect 0 "$key" luks_show_recovery_key "$key" true

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

expect 2 'Usage' ./setup-backup.sh
expect 2 'Usage' ./setup-backup.sh start
expect 2 'Usage' ./setup-backup.sh on
expect 2 'Usage' ./setup-backup.sh on /mnt/a /mnt/b
expect 2 'Usage' ./setup-backup.sh on --force /mnt/backup
expect 2 'Usage' ./setup-backup.sh off now
# shellcheck disable=SC2016 # the $(id) is meant literally
for bad in relative/path 'ftp:host:x' '/mnt/backup x' '/mnt/$(id)' 'sftp:host:"x"' 's3:' '/'; do
	expect 2 'is not a repository' ./setup-backup.sh on --print "$bad"
done
expect 2 'has . or ..' ./setup-backup.sh on --print /mnt/backup/../../srv/data-den
for inside in /srv/data-den /srv/data-den/ /srv/data-den/backup; do
	expect 2 'on the mirror itself' ./setup-backup.sh on --print "$inside"
done
expect 2 'needs a number' ./setup-backup.sh on --print --keep-daily 0 /mnt/backup
expect 2 'needs a number' ./setup-backup.sh on --print --keep-weekly x /mnt/backup
expect 2 'needs a number' ./setup-backup.sh on --print /mnt/backup --keep-monthly
expect 0 'RESTIC_REPOSITORY=/mnt/backup/data-den' ./setup-backup.sh on --print /mnt/backup/data-den/
expect 0 'RESTIC_REPOSITORY=sftp:den@backup.example:data-den' ./setup-backup.sh on --print sftp:den@backup.example:data-den
expect 0 'BACKUP_KEEP_DAILY=14' ./setup-backup.sh on --print --keep-daily 14 /mnt/backup
expect 0 'BACKUP_KEEP_MONTHLY=12' ./setup-backup.sh on --print /mnt/backup

# The backup run refuses to start without its settings.
expect 1 'RESTIC_REPOSITORY is not set' env -u RESTIC_REPOSITORY ./backup.sh

expect 2 'Usage' ./setup-updates.sh
expect 2 'Usage' ./setup-updates.sh start
expect 2 'Usage' ./setup-updates.sh on --force
expect 2 'Usage' ./setup-updates.sh off now
expect 2 'Usage' ./setup-updates.sh status now
for bad in 5:00 24:00 05:60 'x' '05:00"; evil'; do
	expect 2 'needs a time' ./setup-updates.sh on --print --reboot "$bad"
done
expect 2 'needs a time' ./setup-updates.sh on --print --reboot
expect 0 'Automatic-Reboot-Time "05:00"' ./setup-updates.sh on --print
expect 0 'Automatic-Reboot-Time "04:30"' ./setup-updates.sh on --print --reboot 04:30
expect 0 'Automatic-Reboot "false"' ./setup-updates.sh on --print --no-reboot
expect 0 'APT::Periodic::Unattended-Upgrade "1"' ./setup-updates.sh on --print
# shellcheck disable=SC2016 # the ${distro_codename} is meant literally
expect 0 'codename=${distro_codename}-security' ./setup-updates.sh on --print

exit "$failed"
