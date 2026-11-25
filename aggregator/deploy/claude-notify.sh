#!/bin/sh
# Claude Code hooks -> deskpanel (Linux/macOS twin of claude-notify.ps1). Needs curl and jq.
#   claude-notify.sh notify   (Notification)      "<project>: <message>"; permission prompts amber
#   claude-notify.sh start    (UserPromptSubmit)  remembers when this session's turn began
#   claude-notify.sh stop     (Stop)              "<project>: Claude finished (14 min)" if >= 2 min
# Install with install-claude-hooks.sh. Hooks run "async", and this always exits 0.
AGG=${DESKPANEL_URL:-$(cat "$HOME/.claude/deskpanel-notify-url" 2>/dev/null)}
AGG=${AGG:-http://localhost:8088}
LONG=${DESKPANEL_LONG_TURN_SEC:-120} # "finished" only for turns at least this long

in=$(cat)
field() { printf '%s' "$in" | jq -r "$1 // empty" 2>/dev/null; }
cwd=$(field .cwd)
prefix=${cwd:+$(basename "$cwd"): }
state="${TMPDIR:-/tmp}/claude-turn-$(field .session_id | tr -cd 'A-Za-z0-9-')"

send() {
    token=$(cat "$HOME/.claude/deskpanel-notify-token" 2>/dev/null) || return
    jq -nc --arg m "$1" --arg l "$2" '{src: "Claude", msg: $m, lvl: $l}' |
        curl -s -m 3 -o /dev/null -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
            --data-binary @- "$AGG/api/v1/notify"
}

case ${1:-notify} in
start)
    date +%s >"$state"
    ;;
stop)
    [ -f "$state" ] || exit 0
    took=$(($(date +%s) - $(cat "$state")))
    rm -f "$state"
    [ "$took" -ge "$LONG" ] || exit 0
    m=$((took / 60))
    if [ "$m" -ge 60 ]; then len="$((m / 60))h $((m % 60))m"; else len="$m min"; fi
    send "${prefix}Claude finished ($len)" info
    ;;
*)
    msg=$(field .message)
    msg=${msg:-Claude wants your attention}
    case $msg in *permission*) lvl=warn ;; *) lvl=info ;; esac
    send "$prefix$msg" "$lvl"
    ;;
esac
exit 0
