<#
.SYNOPSIS
Hyper-V key-value exchange with the DATA-DEN VM, without any network.

.DESCRIPTION
Dot-sourced by Watch-DataDenDisks.ps1 and Deploy-DataDen.ps1. Send-KvpItem
hands a value to the VM, which hv_kvp_daemon writes into
/var/lib/hyperv/.kvp_pool_0; Get-KvpGuestItem reads what the VM wrote into
/var/lib/hyperv/.kvp_pool_1. Both need an administrator (or SYSTEM).
#>

$script:KvpNamespace = 'root/virtualization/v2'

# The VM's WMI object, or an error naming the -Name parameter.
function Get-KvpVM {
    param([Parameter(Mandatory)] [string] $VMName)

    $vm = Get-CimInstance -Namespace $script:KvpNamespace -ClassName Msvm_ComputerSystem -Filter "ElementName='$($VMName -replace "'", "''")'"
    if (-not $vm) {
        throw "No VM named '$VMName'. Give its name with -Name."
    }
    return $vm
}

# Sends one key-value pair to the VM, replacing the old value.
function Send-KvpItem {
    param(
        [Parameter(Mandatory)] [string] $VMName,
        [Parameter(Mandatory)] [string] $Key,
        [Parameter(Mandatory)] [string] $Value
    )

    $service = Get-CimInstance -Namespace $script:KvpNamespace -ClassName Msvm_VirtualSystemManagementService
    $vm = Get-KvpVM -VMName $VMName
    $class = Get-CimClass -Namespace $script:KvpNamespace -ClassName Msvm_KvpExchangeDataItem
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

# The value the VM wrote under a key, or $null when there is none.
function Get-KvpGuestItem {
    param(
        [Parameter(Mandatory)] [string] $VMName,
        [Parameter(Mandatory)] [string] $Key
    )

    $vm = Get-KvpVM -VMName $VMName
    $component = Get-CimAssociatedInstance -InputObject $vm -ResultClassName Msvm_KvpExchangeComponent
    foreach ($item in @($component.GuestExchangeItems)) {
        if (-not $item) {
            continue
        }
        # Each item is an XML instance of Msvm_KvpExchangeDataItem.
        $properties = ([xml] $item).INSTANCE.PROPERTY
        $name = ($properties | Where-Object NAME -EQ 'Name').VALUE
        if ($name -eq $Key) {
            return ($properties | Where-Object NAME -EQ 'Data').VALUE
        }
    }
    return $null
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
