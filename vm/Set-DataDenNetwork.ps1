<#
.SYNOPSIS
Lets only the Pi into the DATA-DEN VM, at the Hyper-V switch port.

.DESCRIPTION
The Windows Firewall does not see the traffic of a VM on an external switch,
so the VM's network adapter gets extended port ACLs instead: from the Pi only
DATA-DEN's HTTPS (8443), SSH (22) and ping get in, plus DHCP. The VM may
connect out (for updates and images), and the answers to that come back in.
Run it again when the Pi's address changes. Guide: vm/README.md

.EXAMPLE
.\vm\Set-DataDenNetwork.ps1 -PiAddress 192.168.1.20
#>
#Requires -RunAsAdministrator
#Requires -Modules Hyper-V
[CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
param(
    [Parameter(Mandatory)]
    [ipaddress] $PiAddress,

    [string] $Name = 'DATA-DEN'
)

$ErrorActionPreference = 'Stop'

$adapter = Get-VMNetworkAdapter -VMName $Name
if (-not $PSCmdlet.ShouldProcess("VM '$Name'", "Replace the port ACLs: only $PiAddress gets in")) {
    return
}

# The VM may not hand out addresses or announce itself as a router.
Set-VMNetworkAdapter -VMNetworkAdapter $adapter -DhcpGuard On -RouterGuard On -MacAddressSpoofing Off

Get-VMNetworkAdapterExtendedAcl -VMNetworkAdapter $adapter | Remove-VMNetworkAdapterExtendedAcl

# The rule with the highest weight that matches a packet decides.
$pi = $PiAddress.IPAddressToString
$rules = @(
    @{ Direction = 'Inbound'; Action = 'Deny'; Weight = 1 }
    @{ Direction = 'Outbound'; Action = 'Deny'; Weight = 1 }
    # Out to anywhere; stateful, so the answers come back in.
    @{ Direction = 'Outbound'; Action = 'Allow'; Weight = 10; Protocol = 'TCP'; Stateful = $true; IdleSessionTimeout = 3600 }
    @{ Direction = 'Outbound'; Action = 'Allow'; Weight = 11; Protocol = 'UDP'; Stateful = $true; IdleSessionTimeout = 60 }
    # The router's DHCP answers, which may be broadcast.
    @{ Direction = 'Inbound'; Action = 'Allow'; Weight = 20; Protocol = 'UDP'; LocalPort = '68'; RemotePort = '67' }
    @{ Direction = 'Inbound'; Action = 'Allow'; Weight = 30; Protocol = 'TCP'; RemoteIPAddress = $pi; LocalPort = '8443' }
    @{ Direction = 'Inbound'; Action = 'Allow'; Weight = 31; Protocol = 'TCP'; RemoteIPAddress = $pi; LocalPort = '22' }
    @{ Direction = 'Inbound'; Action = 'Allow'; Weight = 32; Protocol = '1'; RemoteIPAddress = $pi }
    @{ Direction = 'Outbound'; Action = 'Allow'; Weight = 32; Protocol = '1'; RemoteIPAddress = $pi }
)
foreach ($rule in $rules) {
    Add-VMNetworkAdapterExtendedAcl -VMNetworkAdapter $adapter @rule
}

Get-VMNetworkAdapterExtendedAcl -VMNetworkAdapter $adapter |
    Sort-Object Weight -Descending |
    Format-Table Weight, Direction, Action, Protocol, RemoteIPAddress, LocalPort, RemotePort, Stateful -AutoSize
