#!/bin/sh
# Install the deskpanel Claude Code hooks for this user (Linux/macOS): "needs your attention", and
# "finished" for turns over 2 minutes. Safe to re-run: it replaces its own hooks, keeps the rest,
# and backs up settings.json first. Needs jq and curl.
#   sh install-claude-hooks.sh [path/to/aggregator/.env]   (default: ../.env next to this script)
# With the .env it writes ~/.claude/deskpanel-notify-token and -url (from NOTIFY_TOKEN and LISTEN);
# without it, put those two files there yourself first.
# Then run /hooks once in an already-open Claude Code session (or restart it) to load them.
set -e
here=$(cd "$(dirname "$0")" && pwd)
envfile=${1:-$here/../.env}
mkdir -p "$HOME/.claude/hooks"
install -m 755 "$here/claude-notify.sh" "$HOME/.claude/hooks/claude-notify.sh"

if [ -f "$envfile" ]; then
    (umask 077 && sed -n 's/^NOTIFY_TOKEN=//p' "$envfile" >"$HOME/.claude/deskpanel-notify-token")
    listen=$(sed -n 's/^LISTEN=//p' "$envfile")
    case $listen in
    "" | :*) echo "http://localhost${listen:-:8088}" >"$HOME/.claude/deskpanel-notify-url" ;;
    *) echo "http://$listen" >"$HOME/.claude/deskpanel-notify-url" ;;
    esac
fi
[ -s "$HOME/.claude/deskpanel-notify-token" ] || {
    echo "No NOTIFY_TOKEN: put the aggregator's NOTIFY_TOKEN in ~/.claude/deskpanel-notify-token" >&2
    exit 1
}
[ -s "$HOME/.claude/deskpanel-notify-url" ] ||
    echo "Note: no ~/.claude/deskpanel-notify-url; the hooks will use http://localhost:8088" >&2

settings=$HOME/.claude/settings.json
[ -f "$settings" ] || echo '{}' >"$settings"
cp "$settings" "$settings.bak"
jq --arg h "$HOME/.claude/hooks/claude-notify.sh" '
  def ours: any(.hooks[]?; (.command // "") | contains("claude-notify"));
  def add($ev; $mode):
    .hooks[$ev] = ([(.hooks[$ev] // [])[] | select(ours | not)]
      + [{hooks: [{type: "command", command: "\($h) \($mode)", timeout: 10, async: true}]}]);
  .hooks //= {} | add("Notification"; "notify") | add("UserPromptSubmit"; "start") | add("Stop"; "stop")
' "$settings.bak" >"$settings"
echo "deskpanel hooks installed in $settings (backup: $settings.bak)"
