#!/usr/bin/env bash
# Shared by setup-raid.sh, encrypt-raid.sh and disk-key.sh: the encryption of
# the mirror with LUKS2. The mirror is unlocked at boot by the VM's virtual
# TPM; a recovery key, shown once, opens it when the TPM cannot.
# Guide: vm/README.md#encryption

# shellcheck disable=SC2034 # used by the scripts that source this file
mapper=data-den
mapper_dev=/dev/mapper/$mapper
# PCR 7 is the Secure Boot state: the key is only released while the VM boots
# with Secure Boot on, and kernel updates do not change it.
tpm_pcrs=7

# luks_install_tools: cryptsetup, systemd's crypttab support and the TPM
# libraries, without the initramfs hooks (the mirror is not the root disk).
luks_install_tools() {
	local packages=(cryptsetup tpm2-tools)
	# Debian 13 ships systemd's cryptsetup support as its own package.
	if apt-cache show systemd-cryptsetup >/dev/null 2>&1; then
		packages+=(systemd-cryptsetup)
	fi
	DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${packages[@]}"
}

# luks_require_tpm: the VM's virtual TPM is there and usable.
luks_require_tpm() {
	if [[ ! -e /dev/tpmrm0 ]] || ! systemd-cryptenroll --tpm2-device=list >/dev/null 2>&1; then
		cat >&2 <<-'EOF'
			The VM has no TPM, so the mirror could not unlock itself at boot.
			Switch the virtual TPM on in Windows, in PowerShell as administrator:
			  Stop-VM -Name DATA-DEN
			  Set-VMKeyProtector -VMName DATA-DEN -NewLocalKeyProtector
			  Enable-VMTPM -VMName DATA-DEN
			  Start-VM -Name DATA-DEN
			Then run this again. Guide: vm/README.md#encryption
		EOF
		exit 1
	fi
}

# luks_crypttab_line UUID: the /etc/crypttab entry. headless: when the TPM
# cannot unlock, give up instead of waiting at the console for a key, so the
# VM still boots and `disk-key.sh unlock` opens the mirror.
luks_crypttab_line() {
	printf '%s UUID=%s none luks,tpm2-device=auto,headless=true,nofail\n' "$mapper" "$1"
}

# luks_fstab_line MOUNTPOINT: the /etc/fstab entry. nofail: the VM boots
# without the data when the mirror stays locked; Docker waits for the mount,
# so DATA-DEN does not start on the empty folder below it.
luks_fstab_line() {
	printf '%s %s ext4 defaults,noatime,nofail,x-systemd.before=docker.service 0 2\n' "$mapper_dev" "$1"
}

# luks_is_recovery_key TEXT: TEXT looks like a key from systemd-cryptenroll
# (eight groups of eight letters of its alphabet).
luks_is_recovery_key() {
	[[ "$1" =~ ^[cbdefghijklnrtuv]{8}(-[cbdefghijklnrtuv]{8}){7}$ ]]
}

# luks_show_recovery_key KEY YES: shows the key once and, unless YES is true,
# has it confirmed, so nobody goes on without having saved it.
luks_show_recovery_key() {
	local key="$1" yes="$2" answer
	cat <<-EOF

		Recovery key of the mirror, shown only now:

		    $key

		Keep it outside the VM, such as in a password manager. Without it the
		data cannot be read when the TPM is lost (a new VM, Windows reinstalled).

	EOF
	$yes && return
	while true; do
		read -rp "Type the first group of the key (${key%%-*}) once it is saved: " answer || return 1
		[[ "$answer" == "${key%%-*}" ]] && break
		echo "That is not the first group; try again." >&2
	done
}

# luks_new_recovery_key DEVICE KEYFILE YES: enrolls a recovery key, unlocking
# with KEYFILE (empty: with the TPM), and shows it.
luks_new_recovery_key() {
	local device="$1" keyfile="$2" yes="$3" key unlock
	if [[ -n "$keyfile" ]]; then
		unlock=--unlock-key-file="$keyfile"
	else
		unlock=--unlock-tpm2-device=auto
	fi
	key="$(systemd-cryptenroll "$unlock" --recovery-key "$device" </dev/null)"
	key="$(grep -oE '[a-z]{8}(-[a-z]{8}){7}' <<< "$key" | head -n 1)"
	if ! luks_is_recovery_key "$key"; then
		echo "systemd-cryptenroll gave no recovery key; check: systemd-cryptenroll $device" >&2
		return 1
	fi
	luks_show_recovery_key "$key" "$yes"
}

# luks_format DEVICE YES: encrypts DEVICE (ERASES it) and opens it as
# /dev/mapper/data-den. Only two ways in remain: the TPM and the recovery key.
luks_format() {
	local device="$1" yes="$2" keyfile
	keyfile="$(mktemp /run/data-den-key.XXXXXX)"
	# The setup key lives only in RAM and only until the TPM is enrolled.
	# shellcheck disable=SC2064 # expand now: keyfile is local
	trap "shred -u '$keyfile' 2>/dev/null || rm -f '$keyfile'" EXIT
	head -c 64 /dev/urandom > "$keyfile"
	cryptsetup luksFormat --batch-mode --type luks2 --label data-den --key-file "$keyfile" "$device"
	cryptsetup open --key-file "$keyfile" "$device" "$mapper"
	luks_new_recovery_key "$device" "$keyfile" "$yes"
	# Enrolls the TPM, then wipes the setup key's slot.
	systemd-cryptenroll --unlock-key-file="$keyfile" --tpm2-device=auto \
		--tpm2-pcrs="$tpm_pcrs" --wipe-slot=password "$device"
	shred -u "$keyfile" 2>/dev/null || rm -f "$keyfile"
	trap - EXIT
}

# luks_write_config DEVICE MOUNTPOINT: crypttab and fstab for the encrypted
# mirror, replacing any older entry for the mount point.
luks_write_config() {
	local device="$1" mountpoint="$2" uuid
	uuid="$(cryptsetup luksUUID "$device")"
	touch /etc/crypttab
	sed -i "/^${mapper}[[:space:]]/d" /etc/crypttab
	luks_crypttab_line "$uuid" >> /etc/crypttab
	sed -i "\#^[^[:space:]]\+[[:space:]]\+${mountpoint}[[:space:]]#d" /etc/fstab
	luks_fstab_line "$mountpoint" >> /etc/fstab
	systemctl daemon-reload
}

# luks_guard_mountpoint MOUNTPOINT: while the mirror is not mounted, nothing
# can write into the folder below it, which is on the VM's small system disk.
luks_guard_mountpoint() {
	mkdir -p "$1"
	if ! mountpoint -q "$1" && ! chattr +i "$1"; then
		echo "Could not protect the empty $1 (chattr +i); go on without." >&2
	fi
}
