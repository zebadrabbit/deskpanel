# Claude Code hooks -> deskpanel (Windows). Puts Claude on the desk panel when a fullscreen app
# hides the taskbar:
#   -Event notify  (Notification)      "<project>: <message>"; permission prompts amber
#   -Event start   (UserPromptSubmit)  remembers when this session's turn began
#   -Event stop    (Stop)              "<project>: Claude finished (14 min)" if the turn ran >= 2 min
# Run the hooks with "async": true so they never slow Claude down; this script always exits 0.
# Linux/macOS twin: claude-notify.sh (install with install-claude-hooks.sh).
#
# Install:
#   1. Copy this file to %USERPROFILE%\.claude\hooks\.
#   2. Put NOTIFY_TOKEN from aggregator/.env in %USERPROFILE%\.claude\deskpanel-notify-token.
#   3. Put the aggregator's URL (http://<server-ip>:8088) in %USERPROFILE%\.claude\deskpanel-notify-url.
#   4. Add to ~/.claude/settings.json, once per event (Notification -Event notify, UserPromptSubmit
#      -Event start, Stop -Event stop):
#        { "hooks": [ { "type": "command", "async": true, "timeout": 10, "command":
#          "powershell.exe -NoProfile -ExecutionPolicy Bypass -File C:/Users/<you>/.claude/hooks/claude-notify.ps1 -Event notify" } ] }
param(
    [ValidateSet("notify", "start", "stop")][string]$Event = "notify",
    [string]$Aggregator, # default: the URL in ~/.claude/deskpanel-notify-url
    [int]$LongTurnSec = 120 # "finished" only for turns at least this long
)
if (-not $Aggregator) {
    $urlFile = "$env:USERPROFILE\.claude\deskpanel-notify-url"
    $Aggregator = if (Test-Path $urlFile) { (Get-Content -Raw $urlFile).Trim() } else { "http://localhost:8088" }
}

function Send-Note([string]$msg, [string]$lvl) {
    $token = (Get-Content -Raw "$env:USERPROFILE\.claude\deskpanel-notify-token").Trim()
    $body = @{ src = "Claude"; msg = $msg; lvl = $lvl } | ConvertTo-Json -Compress
    Invoke-RestMethod -Method Post -Uri "$Aggregator/api/v1/notify" -TimeoutSec 3 `
        -Headers @{ Authorization = "Bearer $token" } -ContentType "application/json; charset=utf-8" `
        -Body ([Text.Encoding]::UTF8.GetBytes($body)) | Out-Null
}

try {
    [Console]::InputEncoding = [Text.Encoding]::UTF8
    $hook = [Console]::In.ReadToEnd() | ConvertFrom-Json
    $prefix = if ($hook.cwd) { (Split-Path -Leaf $hook.cwd) + ": " } else { "" }
    $session = ($hook.session_id -replace "[^A-Za-z0-9-]", "")
    $state = Join-Path $env:TEMP "claude-turn-$session.txt"
    $now = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()

    switch ($Event) {
        "start" { Set-Content -Path $state -Value $now }
        "stop" {
            if (Test-Path $state) {
                $took = $now - [long](Get-Content $state)
                Remove-Item $state
                if ($took -ge $LongTurnSec) {
                    $m = [math]::Floor($took / 60)
                    $len = if ($m -ge 60) { "{0}h {1}m" -f [math]::Floor($m / 60), ($m % 60) } else { "$m min" }
                    Send-Note "${prefix}Claude finished ($len)" "info"
                }
            }
        }
        default {
            $text = if ($hook.message) { $hook.message } else { "Claude wants your attention" }
            Send-Note "$prefix$text" $(if ($text -match "permission") { "warn" } else { "info" })
        }
    }
} catch {
    [Console]::Error.WriteLine("deskpanel notify: $($_.Exception.Message)")
}
exit 0
