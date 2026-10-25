<#
.SYNOPSIS
Creates the DATA-DEN storage VM in Hyper-V and passes both data disks through.

.DESCRIPTION
Takes the two data disks offline in Windows and gives them to a new
generation 2 VM, so only the VM sees them. The VM boots the Debian installer
from the ISO. Its network adapter only lets the Pi in (Set-DataDenNetwork.ps1).
Guide: vm/README.md

.EXAMPLE
.\vm\New-DataDenVM.ps1 -DiskNumber 1,2 -IsoPath C:\iso\debian-13-amd64-netinst.iso -SwitchName External -PiAddress 192.168.1.20
#>
#Requires -RunAsAdministrator
#Requires -Modules Hyper-V, Storage
[CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'High')]
param(
    # The two data disks, as Get-Disk numbers them.
    [Parameter(Mandatory)]
    [ValidateCount(2, 2)]
    [int[]] $DiskNumber,

    # The Debian netinst ISO.
    [Parameter(Mandatory)]
    [ValidateScript({ Test-Path -LiteralPath $_ -PathType Leaf })]
    [string] $IsoPath,

    # An external virtual switch, so the Pi can reach the VM.
    [Parameter(Mandatory)]
    [string] $SwitchName,

    # The Pi's address in the home network; nothing else gets in.
    [Parameter(Mandatory)]
    [ipaddress] $PiAddress,

    [string] $Name = 'DATA-DEN',
    [ValidateRange(2, 64)]
    [int] $MemoryGB = 4,
    [ValidateRange(1, 16)]
    [int] $ProcessorCount = 2,
    [ValidateRange(16, 256)]
    [int] $SystemDiskGB = 32
)

$ErrorActionPreference = 'Stop'

if ($DiskNumber[0] -eq $DiskNumber[1]) {
    throw "Give two different disks, not disk $($DiskNumber[0]) twice."
}
if (Get-VM -Name $Name -ErrorAction SilentlyContinue) {
    throw "A VM named '$Name' already exists. Remove it or choose another -Name."
}
$switch = Get-VMSwitch -Name $SwitchName -ErrorAction SilentlyContinue
if (-not $switch) {
    throw "No virtual switch '$SwitchName'. Create an external one in Hyper-V Manager > Virtual Switch Manager."
}
if ($switch.SwitchType -ne 'External') {
    throw "Switch '$SwitchName' is $($switch.SwitchType); the Pi can only reach the VM through an external switch."
}

$disks = foreach ($number in $DiskNumber) {
    $disk = Get-Disk -Number $number
    if ($disk.IsBoot -or $disk.IsSystem) {
        throw "Disk $number holds Windows; pick the data disks (Get-Disk lists them)."
    }
    $disk
}
$disks | Format-Table Number, FriendlyName, SerialNumber, @{ n = 'Size (GB)'; e = { [math]::Round($_.Size / 1GB) } }, PartitionStyle -AutoSize

if (-not $PSCmdlet.ShouldProcess("disks $($DiskNumber -join ' and ')", "Take offline in Windows and pass through to VM '$Name'; the RAID setup in the VM erases them")) {
    return
}

# Windows must let go of a disk before Hyper-V can pass it through.
foreach ($disk in $disks) {
    if (-not $disk.IsOffline) {
        Set-Disk -Number $disk.Number -IsOffline $true
    }
}

$vhdPath = Join-Path (Get-VMHost).VirtualHardDiskPath "$Name-system.vhdx"
New-VM -Name $Name -Generation 2 -MemoryStartupBytes ($MemoryGB * 1GB) `
    -NewVHDPath $vhdPath -NewVHDSizeBytes ($SystemDiskGB * 1GB) -SwitchName $SwitchName | Out-Null

# Fixed memory, no checkpoints (passed-through disks cannot be in one), and the
# VM starts and stops with Windows.
Set-VM -Name $Name -ProcessorCount $ProcessorCount -StaticMemory `
    -CheckpointType Disabled -AutomaticStartAction Start -AutomaticStopAction ShutDown

# Debian boots with Secure Boot under the UEFI CA template.
Set-VMFirmware -VMName $Name -EnableSecureBoot On -SecureBootTemplate MicrosoftUEFICertificateAuthority
$dvd = Add-VMDvdDrive -VMName $Name -Path $IsoPath -Passthru
Set-VMFirmware -VMName $Name -FirstBootDevice $dvd

foreach ($disk in $disks) {
    Add-VMHardDiskDrive -VMName $Name -ControllerType SCSI -DiskNumber $disk.Number
}

& (Join-Path $PSScriptRoot 'Set-DataDenNetwork.ps1') -Name $Name -PiAddress $PiAddress -Confirm:$false

Write-Output "VM '$Name' is ready. Start it and install Debian (vm/README.md, step 3):"
Write-Output "  Start-VM -Name '$Name'; vmconnect localhost '$Name'"
