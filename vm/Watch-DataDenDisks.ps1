<#
.SYNOPSIS
Checks the health of the DATA-DEN data disks and hands it to the VM.

.DESCRIPTION
Hyper-V does not pass the SMART values of a passed-through disk to the VM, but
Windows still reads them. This script looks at each disk the VM has passed
through: the health Windows derives from SMART, uncorrected read and write
errors, the temperature and the wear. It hands the result to the VM over
Hyper-V's key-value exchange (key DataDenDiskHealth), without any network;
vm/disk-health.sh in the VM reads it and DATA-DEN notifies the admins.

-Install registers it as a scheduled task that runs every 15 minutes as
SYSTEM, -Uninstall removes the task. Guide: vm/README.md#disk-health

.EXAMPLE
.\vm\Watch-DataDenDisks.ps1 -Install

.EXAMPLE
.\vm\Watch-DataDenDisks.ps1 -WhatIf
#>
#Requires -RunAsAdministrator
#Requires -Modules Hyper-V, Storage, ScheduledTasks
[CmdletBinding(SupportsShouldProcess, DefaultParameterSetName = 'Check')]
param(
    [string] $Name = 'DATA-DEN',

    # Registers the scheduled task that runs this script every 15 minutes.
    [Parameter(Mandatory, ParameterSetName = 'Install')]
    [switch] $Install,

    # Removes the scheduled task.
    [Parameter(Mandatory, ParameterSetName = 'Uninstall')]
    [switch] $Uninstall,

    # A disk at or above this temperature, in degrees Celsius, is reported.
    [ValidateRange(30, 90)]
    [int] $MaxTemperature = 55
)

$ErrorActionPreference = 'Stop'
$taskName = 'DATA-DEN disk health'
$key = 'DataDenDiskHealth'
$ns = 'root/virtualization/v2'

if ($Install) {
    if ($PSCmdlet.ShouldProcess($taskName, 'Register a scheduled task that runs every 15 minutes as SYSTEM')) {
        # The script stays where it is; the task runs it from there.
        $arguments = "-NoProfile -NonInteractive -ExecutionPolicy Bypass -File `"$PSCommandPath`" -Name `"$Name`" -MaxTemperature $MaxTemperature"
        $action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument $arguments
        $trigger = New-ScheduledTaskTrigger -Once -At (Get-Date) -RepetitionInterval (New-TimeSpan -Minutes 15)
        $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
        $settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -ExecutionTimeLimit (New-TimeSpan -Minutes 5)
        Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
        Start-ScheduledTask -TaskName $taskName
        Write-Output "Scheduled task '$taskName' registered; it checks the disks of '$Name' every 15 minutes."
        Write-Output 'Keep this script where it is: the task runs it from here.'
    }
    return
}
if ($Uninstall) {
    if ($PSCmdlet.ShouldProcess($taskName, 'Remove the scheduled task')) {
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
        Write-Output "Scheduled task '$taskName' removed."
    }
    return
}

# The problems of one disk, in German for the notification.
function Get-DiskProblem {
    param([Parameter(Mandatory)] [int] $Number)

    $physical = Get-PhysicalDisk | Where-Object DeviceId -EQ "$Number"
    if (-not $physical) {
        return , "Windows findet die Platte nicht mehr"
    }
    $problems = [System.Collections.Generic.List[string]]::new()
    if ($physical.HealthStatus -ne 'Healthy') {
        $problems.Add("Zustand laut SMART: $($physical.HealthStatus)")
    }
    $operational = @($physical.OperationalStatus) | Where-Object { $_ -ne 'OK' }
    if ($operational) {
        $problems.Add("Betriebszustand: $($operational -join ', ')")
    }
    $counter = $physical | Get-StorageReliabilityCounter -ErrorAction SilentlyContinue
    if ($counter) {
        if ($counter.ReadErrorsUncorrected -gt 0) {
            $problems.Add("$($counter.ReadErrorsUncorrected) nicht korrigierbare Lesefehler")
        }
        if ($counter.WriteErrorsUncorrected -gt 0) {
            $problems.Add("$($counter.WriteErrorsUncorrected) nicht korrigierbare Schreibfehler")
        }
        if ($counter.Temperature -ge $MaxTemperature) {
            $problems.Add("$($counter.Temperature) Grad Celsius warm")
        }
        if ($counter.Wear -ge 90) {
            $problems.Add("$($counter.Wear) % abgenutzt")
        }
    }
    return , $problems
}

# Sends one key-value pair to the VM, replacing the old value.
function Send-KvpItem {
    param(
        [Parameter(Mandatory)] [string] $VMName,
        [Parameter(Mandatory)] [string] $Key,
        [Parameter(Mandatory)] [string] $Value
    )

    $service = Get-CimInstance -Namespace $ns -ClassName Msvm_VirtualSystemManagementService
    $vm = Get-CimInstance -Namespace $ns -ClassName Msvm_ComputerSystem -Filter "ElementName='$($VMName -replace "'", "''")'"
    if (-not $vm) {
        throw "No VM named '$VMName'. Give its name with -Name."
    }
    $class = Get-CimClass -Namespace $ns -ClassName Msvm_KvpExchangeDataItem
    # Source 0 is the host's pool, which the VM reads as .kvp_pool_0.
    $item = New-CimInstance -CimClass $class -ClientOnly -Property @{ Name = $Key; Data = $Value; Source = [uint16] 0 }
    $serializer = [Microsoft.Management.Infrastructure.Serialization.CimSerializer]::Create()
    $text = [Text.Encoding]::Unicode.GetString($serializer.Serialize($item, [Microsoft.Management.Infrastructure.Serialization.InstanceSerializationOptions]::None))
    # Modify fails while the key does not exist yet; then it is added.
    foreach ($method in 'ModifyKvpItems', 'AddKvpItems') {
        $result = Invoke-CimMethod -InputObject $service -MethodName $method -Arguments @{ TargetSystem = $vm; DataItems = @($text) }
        if (Wait-CimJob -Result $result) {
            return
        }
    }
    throw "Hyper-V did not take the value for '$VMName'. Is the VM running, with Data Exchange on (Get-VMIntegrationService)?"
}

# Waits for a WMI method's job; true when it succeeded.
function Wait-CimJob {
    param([Parameter(Mandatory)] $Result)

    if ($Result.ReturnValue -eq 0) {
        return $true
    }
    if ($Result.ReturnValue -ne 4096) {
        return $false
    }
    $job = Get-CimInstance -InputObject $Result.Job
    # 2 new, 3 starting, 4 running; 7 completed.
    for ($i = 0; $i -lt 60 -and $job.JobState -in 2, 3, 4; $i++) {
        Start-Sleep -Milliseconds 500
        $job = Get-CimInstance -InputObject $job
    }
    return $job.JobState -eq 7
}

$drives = @(Get-VMHardDiskDrive -VMName $Name | Where-Object { $null -ne $_.DiskNumber })
if ($drives.Count -eq 0) {
    throw "VM '$Name' has no passed-through disks. Give its name with -Name."
}
$lines = [System.Collections.Generic.List[string]]::new()
foreach ($drive in $drives) {
    $problems = Get-DiskProblem -Number $drive.DiskNumber
    if ($problems.Count -gt 0) {
        $disk = Get-Disk -Number $drive.DiskNumber -ErrorAction SilentlyContinue
        $label = "Platte $($drive.DiskNumber)"
        if ($disk) {
            $label += " ($($disk.FriendlyName), $($disk.SerialNumber.Trim()))"
        }
        $lines.Add("${label}: $($problems -join '; ')")
    }
}

$status = if ($lines.Count -gt 0) { 'warn' } else { 'ok' }
$value = (@("status=$status", "checked=$((Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ'))") + $lines) -join "`n"
# The VM reads at most 2048 bytes.
if ([Text.Encoding]::UTF8.GetByteCount($value) -gt 2000) {
    $value = $value.Substring(0, 1000) + "`n..."
}
Write-Output $value
if ($PSCmdlet.ShouldProcess("VM '$Name'", "Hand over the disk health as $key")) {
    Send-KvpItem -VMName $Name -Key $key -Value $value
}
