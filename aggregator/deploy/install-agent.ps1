# Install the deskpanel GPU agent on a Windows GPU host. Run from an elevated PowerShell.
# Elevated sessions don't see mapped drives, so run it from a local copy or a UNC path.
#   -Exe         the agent binary (default: next to this script, else ..\dist)
#   -Aggregator  the only address allowed through the firewall to the agent
param(
    [Parameter(Mandatory)][string]$Aggregator, # the aggregator server's IP
    [string]$Exe,
    [int]$Port = 9835
)
$ErrorActionPreference = 'Stop'
if (-not $Exe) {
    $Exe = @("$PSScriptRoot\deskpanel-windows-amd64.exe", "$PSScriptRoot\..\dist\deskpanel-windows-amd64.exe") |
        Where-Object { Test-Path $_ } | Select-Object -First 1
}
if (-not $Exe) { throw "deskpanel-windows-amd64.exe not found; pass -Exe" }

$dir = "$env:ProgramFiles\deskpanel"
New-Item -ItemType Directory -Force $dir | Out-Null
Stop-ScheduledTask -TaskName deskpanel-agent -ErrorAction SilentlyContinue
Get-Process deskpanel -ErrorAction SilentlyContinue | Stop-Process -Force
Copy-Item $Exe "$dir\deskpanel.exe" -Force

Get-NetFirewallRule -DisplayName 'deskpanel agent' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
New-NetFirewallRule -DisplayName 'deskpanel agent' -Direction Inbound -Protocol TCP -LocalPort $Port `
    -RemoteAddress $Aggregator -Program "$dir\deskpanel.exe" -Action Allow | Out-Null

# Runs at boot as SYSTEM (no login needed), restarts if it dies.
$action = New-ScheduledTaskAction -Execute "$dir\deskpanel.exe" -Argument "agent :$Port"
$trigger = New-ScheduledTaskTrigger -AtStartup
$settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 999 `
    -RestartInterval (New-TimeSpan -Minutes 1) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName deskpanel-agent -Action $action -Trigger $trigger -Settings $settings `
    -User SYSTEM -RunLevel Highest -Force | Out-Null
Start-ScheduledTask -TaskName deskpanel-agent
Start-Sleep -Seconds 2
Invoke-RestMethod "http://127.0.0.1:$Port/gpu" | Format-Table host, n, temp, util, vram, vram_t, pwr, procs, st
