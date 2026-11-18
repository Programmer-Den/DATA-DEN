<#
.SYNOPSIS
Puts a new DATA-DEN version into service in the VM, from Windows.

.DESCRIPTION
Hands an image tag (such as main, 1.2.3 or sha-abc1234) to the VM over
Hyper-V's key-value exchange (key DataDenDeploy), without any network into
the VM. vm/deploy.sh in the VM pulls that image, restarts DATA-DEN with it and
waits until it is healthy, or goes back to the version that ran before. This
script waits for its answer (key DataDenDeployResult), prints it, and fails
when the deploy failed.

-Install lets one Windows account, such as a self-hosted runner's, start
deploys without being an administrator: it registers the scheduled task
"DATA-DEN deploy", which runs a copy of this script as SYSTEM, and that
account may only start the task and hand it a tag through a folder. The
account never gets Hyper-V or administrator rights. -Uninstall removes it.

Guide: deploy/README.md#deploy-from-windows

.EXAMPLE
.\vm\Deploy-DataDen.ps1 -Tag main

.EXAMPLE
.\vm\Deploy-DataDen.ps1 -Install -RunnerAccount data-den-deploy
#>
#Requires -RunAsAdministrator
[CmdletBinding(SupportsShouldProcess, DefaultParameterSetName = 'Deploy')]
param(
    # The image tag to run: main, latest, 1.2.3 or sha-abc1234.
    [Parameter(Mandatory, ParameterSetName = 'Deploy')]
    [ValidatePattern('^[a-z0-9][a-z0-9._-]{0,127}$', Options = 'None')]
    [string] $Tag,

    [string] $Name = 'DATA-DEN',

    # How long to wait for the VM's answer.
    [ValidateRange(1, 120)]
    [int] $TimeoutMinutes = 30,

    # Registers the scheduled task that this account may start.
    [Parameter(Mandatory, ParameterSetName = 'Install')]
    [switch] $Install,

    [Parameter(Mandatory, ParameterSetName = 'Install')]
    [string] $RunnerAccount,

    # Removes the scheduled task and its folders.
    [Parameter(Mandatory, ParameterSetName = 'Uninstall')]
    [switch] $Uninstall,

    # What the scheduled task runs: the deploy that the account asked for.
    [Parameter(Mandatory, ParameterSetName = 'FromRequest')]
    [switch] $FromRequest
)

$ErrorActionPreference = 'Stop'
$taskName = 'DATA-DEN deploy'
$installDir = Join-Path $env:ProgramFiles 'DATA-DEN'
$exchangeDir = Join-Path $env:ProgramData 'DATA-DEN\deploy'
# The account writes its request here...
$requestDir = Join-Path $exchangeDir 'request'
# ...and only reads the answer here, which only SYSTEM writes.
$resultDir = Join-Path $exchangeDir 'result'
$tagPattern = '^[a-z0-9][a-z0-9._-]{0,127}$'
$idPattern = '^[A-Za-z0-9-]{1,64}$'

. (Join-Path $PSScriptRoot 'DataDenKvp.ps1')

# Hands the tag to the VM and waits for its answer; returns the answer's text.
function Invoke-DataDenDeploy {
    param(
        [Parameter(Mandatory)] [string] $Id,
        [Parameter(Mandatory)] [string] $DeployTag
    )

    # Starting the same request twice would match the old answer.
    $old = Get-KvpGuestItem -VMName $Name -Key 'DataDenDeployResult'
    if ($old -match "(?m)^id=$Id$") {
        throw "Request $Id was handed over before; give a new one."
    }
    Send-KvpItem -VMName $Name -Key 'DataDenDeploy' -Value "id=$Id`ntag=$DeployTag"
    $deadline = (Get-Date).AddMinutes($TimeoutMinutes)
    while ((Get-Date) -lt $deadline) {
        Start-Sleep -Seconds 5
        $answer = Get-KvpGuestItem -VMName $Name -Key 'DataDenDeployResult'
        if ($answer -match "(?m)^id=$Id$") {
            return $answer
        }
    }
    return "id=$Id`nstatus=failed`ntag=$DeployTag`nThe VM did not answer within $TimeoutMinutes minutes. Is it running, with deploys on (sudo vm/setup-deploy.sh status in the VM)?"
}

if ($Install) {
    $account = Get-LocalUser -Name $RunnerAccount -ErrorAction SilentlyContinue
    if (-not $account) {
        throw "No local account '$RunnerAccount'. Create it first, or give the runner's account with -RunnerAccount."
    }
    $sid = $account.SID.Value
    if ($PSCmdlet.ShouldProcess($taskName, "Register a scheduled task as SYSTEM that '$RunnerAccount' may start")) {
        # The task runs a copy only administrators can change, never this
        # file, wherever it is.
        New-Item -ItemType Directory -Force -Path $installDir | Out-Null
        Copy-Item -Path $PSCommandPath, (Join-Path $PSScriptRoot 'DataDenKvp.ps1') -Destination $installDir -Force

        # Only SYSTEM and administrators, plus the account: it may write
        # requests and read answers, nothing else.
        foreach ($dir in $exchangeDir, $requestDir, $resultDir) {
            New-Item -ItemType Directory -Force -Path $dir | Out-Null
        }
        $rights = [Security.AccessControl.FileSystemRights]
        $inherit = [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'
        $none = [Security.AccessControl.PropagationFlags]::None
        $allow = [Security.AccessControl.AccessControlType]::Allow
        $admins = New-Object Security.Principal.SecurityIdentifier 'S-1-5-32-544'
        $system = New-Object Security.Principal.SecurityIdentifier 'S-1-5-18'
        $user = New-Object Security.Principal.SecurityIdentifier $sid
        foreach ($entry in @(
                @{ Path = $exchangeDir; Rights = $rights::ReadAndExecute; Inherit = [Security.AccessControl.InheritanceFlags]::None },
                @{ Path = $requestDir; Rights = $rights::Modify; Inherit = $inherit },
                @{ Path = $resultDir; Rights = $rights::ReadAndExecute; Inherit = $inherit })) {
            $acl = New-Object Security.AccessControl.DirectorySecurity
            $acl.SetAccessRuleProtection($true, $false)
            $acl.SetOwner($admins)
            foreach ($full in $admins, $system) {
                $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($full, $rights::FullControl, $inherit, $none, $allow)))
            }
            $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($user, $entry.Rights, $entry.Inherit, $none, $allow)))
            Set-Acl -Path $entry.Path -AclObject $acl
        }

        $script = Join-Path $installDir (Split-Path -Leaf $PSCommandPath)
        $arguments = "-NoProfile -NonInteractive -ExecutionPolicy Bypass -File `"$script`" -FromRequest -Name `"$Name`" -TimeoutMinutes $TimeoutMinutes"
        $action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument $arguments
        $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
        $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan -Minutes ($TimeoutMinutes + 10)) -MultipleInstances IgnoreNew
        Register-ScheduledTask -TaskName $taskName -Action $action -Principal $principal -Settings $settings -Force | Out-Null

        # Lets the account read and start the task (schtasks /Run), but not
        # change it.
        $scheduler = New-Object -ComObject Schedule.Service
        $scheduler.Connect()
        $task = $scheduler.GetFolder('\').GetTask($taskName)
        $dacl = $task.GetSecurityDescriptor(4)
        if ($dacl -notmatch [regex]::Escape(";;;$sid)")) {
            $task.SetSecurityDescriptor("$dacl(A;;0x1200a9;;;$sid)", 0)
        }
        Write-Output "Scheduled task '$taskName' registered; '$RunnerAccount' may start deploys of '$Name'."
        Write-Output "It hands a tag over by writing $requestDir\request.txt and running: schtasks /Run /TN `"$taskName`""
        Write-Output "Run -Install again after updating this repository, so the task runs the new script."
    }
    return
}

if ($Uninstall) {
    if ($PSCmdlet.ShouldProcess($taskName, 'Remove the scheduled task and its folders')) {
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
        Remove-Item -Recurse -Force -Path $exchangeDir, $installDir -ErrorAction SilentlyContinue
        Write-Output "Scheduled task '$taskName' removed."
    }
    return
}

if ($FromRequest) {
    $requestFile = Join-Path $requestDir 'request.txt'
    $resultFile = Join-Path $resultDir 'result.txt'
    $item = Get-Item -LiteralPath $requestFile -Force -ErrorAction SilentlyContinue
    # The account controls this file: no links, nothing large, and only the
    # two lines it may give.
    $request = $null
    if ($item -is [IO.FileInfo] -and -not ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -and $item.Length -le 512) {
        $request = Get-Content -LiteralPath $requestFile -Raw
    }
    Remove-Item -LiteralPath $requestFile -Force -ErrorAction SilentlyContinue
    $id = $null
    $requestTag = $null
    if ($request) {
        foreach ($line in $request -split "\r?\n") {
            if ($line -match '^id=(.*)$') { $id = $Matches[1] }
            elseif ($line -match '^tag=(.*)$') { $requestTag = $Matches[1] }
        }
    }
    if ($id -cnotmatch $idPattern -or $requestTag -cnotmatch $tagPattern) {
        $answer = "id=unknown`nstatus=failed`nThe request in $requestFile is not understood; it needs the lines id=<letters, digits, -> and tag=<image tag>."
    } else {
        try {
            $answer = Invoke-DataDenDeploy -Id $id -DeployTag $requestTag
        } catch {
            $answer = "id=$id`nstatus=failed`ntag=$requestTag`n$($_.Exception.Message)"
        }
    }
    # Written beside it and moved, so the account never reads half of it.
    $partial = Join-Path $resultDir 'result.tmp'
    Set-Content -LiteralPath $partial -Value $answer -Encoding UTF8
    Move-Item -LiteralPath $partial -Destination $resultFile -Force
    return
}

$answer = Invoke-DataDenDeploy -Id ('manual-' + (Get-Date).ToUniversalTime().ToString('yyyyMMddHHmmss')) -DeployTag $Tag
Write-Output $answer
if ($answer -notmatch '(?m)^status=ok$') {
    exit 1
}
