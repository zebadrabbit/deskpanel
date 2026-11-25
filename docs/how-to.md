# How do I…

`<server-ip>`, `<repo>`, **the server** and **the PC** mean the same on every page: see [Words used in these pages](README.md#words-used-in-these-pages). [Back to the index](README.md).

**Recipes on other pages**

- Install, first boot, [change the panel's Wi-Fi, aggregator address or token](getting-started.md#settingstxt):
  [Getting started](getting-started.md)
- [Set up the weather?](screen.md#set-up-the-weather) and
  [See what a tornado warning looks like?](screen.md#see-what-a-tornado-warning-looks-like)
- [Edit your tabs in the browser?](editor.md#edit-your-tabs-in-the-browser)
- [Make my own tab?](layout.md#make-my-own-tab)
- [Change how the idle tour behaves?](layout.md#change-how-the-idle-tour-behaves)
- [Show a Home Assistant sensor on the panel?](cards.md#general-cards-home-assistant)
- [Show the fan speed?](cards.md#gpu-cards-choose-what-they-show)

## Show a Planka board on the panel?

In Planka, open the board, open its members (the avatars at the
top), and add **deskpanel** as an **Editor**; editor rights are what let Done work. It shows up within
about 30 s. Remove deskpanel to hide the board again. Up to 6 boards fit.

## Why did the panel restart?

Four places keep the answer.

- **The bell.** An amber "Panel restarted after a crash (task watchdog), up 3 d 4 h before" means the
  panel crashed or lost power for a moment. Power-ons, flashing and restarts aren't announced.
- **The aggregator's log.** Every boot it saw: `cd <repo>/aggregator && docker compose logs | grep "panel: boot"`.
- **The serial log.** `python -m serial.tools.miniterm --raw COMx 115200` shows `boot 12: …` at start.
  A crash prints its backtrace just before.
- **The SD card.** Power off, take the card out, open `boot.log`. One line per boot, newest last, at least
  160 kept. The boot number counts from the last power-on.

What to do:

- A crash or watchdog: note the time, keep the serial log, and [open an issue](https://github.com/zebadrabbit/deskpanel/issues) with both.
- A brownout: the USB supply or cable is weak, so try another.
- Only power-ons: the power was cut.

## Put my own message on the panel?

On the server:

```sh
cd <repo>/aggregator
N=$(grep ^NOTIFY_TOKEN= .env | cut -d= -f2)
curl -X POST -H "Authorization: Bearer $N" -d '{"src":"Me","msg":"Take the bread out"}' http://<server-ip>:8088/api/v1/notify
```

Add `"lvl":"warn"` for amber or `"lvl":"alert"` for red. Messages longer than 88 bytes get cut.

## Send notifications from Uptime Kuma, Grafana, Prometheus or anything that speaks ntfy?

- **ntfy clients** (Uptime Kuma's "ntfy" notification type, scripts, CI): use server
  `http://<server-ip>:8088`, any topic, and `NOTIFY_TOKEN` as the access token.
  - Priority 5 = red, 4 = amber, the rest blue.
  - `curl -H "Authorization: Bearer $N" -H "Title: Backup" -d "done in 18 min" http://<server-ip>:8088/backups`
- **Prometheus Alertmanager**: add a receiver:

  ```yaml
  receivers:
    - name: deskpanel
      webhook_configs:
        - url: http://<server-ip>:8088/api/v1/alertmanager
          http_config:
            authorization: { credentials: <NOTIFY_TOKEN> }
  ```

  Firing alerts are amber (red with `severity: critical`), resolved ones blue.
- **Grafana alerting**: add a contact point of type **Webhook**:
  - URL `http://<server-ip>:8088/api/v1/alertmanager`
  - Authorization header scheme `Bearer`, credentials `NOTIFY_TOKEN`

## Get Claude notifications on a computer?

- **Linux or macOS**:
  1. Copy `aggregator/deploy/` to it.
  2. Put the server's `NOTIFY_TOKEN` in `~/.claude/deskpanel-notify-token` and
     `http://<server-ip>:8088` in `~/.claude/deskpanel-notify-url`.
  3. Run `sh install-claude-hooks.sh`.
  4. Run `/hooks` in Claude Code once (or restart it).

  On the server itself the installer reads both from `aggregator/.env`:
  `sh <repo>/aggregator/deploy/install-claude-hooks.sh`.
- **Windows**: follow the steps at the top of `aggregator/deploy/claude-notify.ps1`.

## Change how long a task must run before "Claude finished"?

- Linux: set `DESKPANEL_LONG_TURN_SEC=300` (seconds) in the environment Claude Code runs in.
- Windows: add ` -LongTurnSec 300` to the end of the `Stop` hook's command in `~/.claude/settings.json`.

## Turn Claude notifications off?

In Claude Code, run `/hooks` and remove them, or delete the `hooks`
block from `~/.claude/settings.json`. The Linux installer leaves the old version in `settings.json.bak`.

## Add a service dot?

1. Edit `SERVICES` in `<repo>/aggregator/.env`: comma-separated `name=url`, for example
   `SERVICES=jellyfin=http://<nas-ip>:8096/health,plex=http://<nas-ip>:32400/identity`.
   Anything that answers with a 2xx within 3 s counts as up.
2. Then `cd <repo>/aggregator && docker compose up -d --force-recreate`.

## Add another GPU machine?

1. Run the agent on it:
   - Windows: `aggregator/deploy/install-agent.ps1 -Aggregator <server-ip>`, from an elevated
     PowerShell.
   - Linux or TrueNAS: see [aggregator/README.md](../aggregator/README.md).
2. Add `name=http://host:9835` to `GPU_AGENTS` in `.env`.
3. Recreate the container as above.

## Update the panel's firmware?

From the flasher page: flash again from it (`settings.txt` stays on the card).
Built yourself:

1. On the machine that holds the checkout: `sh <repo>/firmware/build.sh`. Never build over a network
   share; the ESP-IDF component manager can't unpack onto one.
2. On the PC: `powershell -ExecutionPolicy Bypass -File <repo>\firmware\flash.ps1 -Port COMx`.
   The panel's log follows the flash; `Ctrl+]` quits it.

## Update the aggregator?

On the server: `cd <repo>/aggregator && sh dist.sh && docker compose up -d --build`.
`dist.sh` runs the tests first. With the prebuilt image: `docker compose -f compose.image.yaml pull && docker
compose -f compose.image.yaml up -d`.

## Updating the GPU agents

Fan speed needs agents that report it (any built since November 2026); an older agent still works and its
fan just reads `Fan --`. Build first (`cd <repo>/aggregator && sh dist.sh`, which leaves `dist/deskpanel-windows-amd64.exe`
and `dist/deskpanel-linux-amd64`), then on each GPU host:
- **TrueNAS:** replace the binary in the dataset (`/mnt/<pool>/deskpanel/deskpanel`, keep it `chmod 755`; copy the
  old one aside first if you like), then restart the Custom App (Apps → the deskpanel agent → Restart). Check it:
  `curl http://<nas-ip>:9835/gpu` should now include `"fan"` for a GPU with a fan.
- **Windows:** copy `dist/deskpanel-windows-amd64.exe` and `aggregator/deploy/install-agent.ps1` to a local folder
  (an elevated PowerShell can't see mapped drives) and re-run `install-agent.ps1 -Aggregator <server-ip>` **as
  administrator**. It replaces the installed copy and restarts the startup task.
- Nothing changes on the aggregator or the panel; the fan shows up on the next poll.

## Change a token?

- `PANEL_TOKEN`:
  1. Put the new value in `aggregator/.env`, and in `settings.txt` on the panel's SD card (`PANEL_TOKEN=…`)
     or `firmware/main/secrets.h`.
  2. Recreate the aggregator.
  3. Restart the panel (`settings.txt`) or build and flash it (`secrets.h`).
- `NOTIFY_TOKEN`:
  1. Put the new value in `.env` and recreate the aggregator.
  2. Update `~/.claude/deskpanel-notify-token` on every computer with the hooks (re-run
     `install-claude-hooks.sh` on Linux).
- `HA_TOKEN`:
  1. Make a new token in Home Assistant, put it in `aggregator/.env` and recreate the aggregator.
  2. Delete the old token in Home Assistant (your name, Security, Long-lived access tokens).
