# aggregator

One Go binary, two modes:

| Mode | Runs on | Does |
|------|---------|------|
| `deskpanel serve` | the home server (Docker Compose) | runs every collector, serves `/api/v1/panel` to the device |
| `deskpanel agent [addr]` | each GPU host | wraps `nvidia-smi -q -x`, serves `GET /gpu` as JSON (default `:9835`): per GPU the `/panel` fields plus `"fan"`, the fan speed in %. `"fan"` is left out when nvidia-smi says N/A (a passively cooled GPU), and an old agent never sends it; `0` is a real reading (zero-RPM mode). `fan` is agent-to-aggregator only: `/api/v1/panel` never carries it |
| `deskpanel demo-json` | anywhere | prints one demo `/api/v1/panel` document (the firmware's PC simulator renders it) |
| `deskpanel demo-weather <dir>` | anywhere | writes the demo Weather tab into `<dir>`: `weather.json` (no warning), `warning.json` (a Tornado Warning over home), `frame-<t>.z` radar frames and `alert-<n>.json` / `warning-alert-<n>.json` texts. The simulator renders them |
| `deskpanel demo-layout <dir>` | anywhere | writes the simulator's demo layouts into `<dir>` as `<name>.json` (`default`, `gpu1`, `gpu4`, `gpu0`, `custom`, `noradar`, `radar-moved`, `sizes`, `longnames`, ...), in the served form. To add one, add an entry to `demoLayouts()` in `layout.go` |
| `deskpanel demo-astro <dir>` | anywhere | writes the astronomical clock for Greenwich into `<dir>`: `astro-night.json` and `sky-night.z` at `DEMO_NOW` (or now), `astro-day.json` and `sky-day.z` 16 h later, as `/api/v1/astro` and `/api/v1/astro/sky/{t}` serve them. The simulator renders them (`sim/run.sh` runs it at `DEMO_NOW` + 45 min, 8:45 PM in Greenwich) |
| `deskpanel layout-default` | the server | collects once from the `.env` sources (GPU agents, TrueNAS) and prints the default layout as `layout.json` text. With no source answering, a template with `<gpu host>` and `<pool>` placeholders |
| `deskpanel editor-demo` | anywhere | prints the screen editor demo's data: `{"layout", "sources", "panel", "cards"}` from the demo collectors at a fixed instant, with a made-up Prometheus and Home Assistant (`demo.go`). Committed as `editor/demo-data.json`; after changing the demo or a table the editor reads, run it in a Linux shell on the server, not Windows PowerShell (its `>` writes UTF-16): `sh dist.sh && ./dist/deskpanel-linux-amd64 editor-demo > editor/demo-data.json`, or without a build, `docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD":/src -w /src golang:1.26-alpine go run . editor-demo > editor/demo-data.json` (`TestDemoDataCurrent` fails until you do) |

**Demo mode**: `DEMO=1` replaces every source with lively fake data (`demo.go`):
- GPUs working
- a backup pool creeping past 85 %
- a service that drops out every few minutes
- a phone coming and going on the tailnet
- two task boards where Done works in memory
- a Claude-style note every 4 minutes
- a Weather tab: moving radar frames, conditions, a Tornado Watch and a Wind Advisory; with
  **`DEMO_WARNING=1`** also a Tornado Warning over home (the takeover) and a warning outline
- the astronomical clock's sky over Greenwich (51.48 N, 0.00 E, times in UTC) at the live time

The event watcher runs on it too, so demo events reach the bell.

Collectors run on their own intervals and cache their last good result. A failed run marks its
section `stale` and keeps serving the old data; nothing ever blocks the device's request.

| Collector | Section.key | Every | Source |
|-----------|-------------|-------|--------|
| TrueNAS | `homelab.nas` | 30 s | JSON-RPC over `wss://…/api/current`, read-only API key, pinned cert |
| GPUs | `homelab.gpu` | 5 s | the agents in `GPU_AGENTS`; an unreachable host shows as `"st":"off"` and never marks the section stale |
| Tailscale | `homelab.ts` | 15 s | local tailscaled LocalAPI over its unix socket |
| Services | `homelab.svc` | 15 s | `GET` each URL in `SERVICES` (e.g. a service running on this host: its `/health`); 2xx within 3 s = up, a down service never marks the section stale |
| Planka | `planka.boards` | 30 s | Planka 2.x REST as the `deskpanel` user |
| Notifications | `notify.notes` | 1 s (+ on change) | log fed by `POST /api/v1/notify` and the event watcher, saved to `NOTES_FILE` (see [Notifications](#notifications)) |
| Conditions | `weather.now` | 10 min | Open-Meteo forecast API at `WEATHER_LAT`/`WEATHER_LON` |
| Radar | `weather.frames` | 150 s | IEM NEXRAD N0Q composites over an OpenStreetMap base map (darkened on the server) (see [Weather](#weather)) |
| NWS alerts | `weather.alerts` | 1 min | `api.weather.gov` `/alerts/active`, `status=actual` only |
| Astro | its own route | 1 min (times, phase, overhead), 2 min (sky picture) | worked out here from `WEATHER_LAT`/`WEATHER_LON` and the star data in `skydata/`; no network (see [Astronomical clock](#astronomical-clock)) |

The weather collectors are off unless `WEATHER_LAT` is set, and their slots stay out of `/api/v1/panel`
(it has an 8 KB budget); they're served by the `/api/v1/weather` routes below.

**Events.** A watcher (`events.go`, every 10 s) compares the collectors' results with the last look
and posts a note on real changes:
- a service down (red) or back (blue)
- a pool unhealthy (red) or healthy again (blue)
- a pool rising past 85 % (amber) or 95 % (red)
- each new TrueNAS WARNING (amber) or ERROR+ (red)
- a GPU at 90 °C or more (red)
- TrueNAS, Tailscale or Planka failing for 2+ minutes (amber), and answering again (blue)
- NWS alerts failing for **10+ minutes** (amber "Can't reach NWS…"), and answering again (blue)
- each new NWS alert at home: warning red, watch amber, advisory blue. A Tornado or Extreme Wind
  Warning's note carries the action `wx`, which makes the panel jump to the Weather tab and show the
  sticky notice. A rising escalation (tornado observed, considerable damage threat, emergency) posts
  another red note, and "… ended" (blue) follows when that warning expires or is cancelled. A warning
  upgraded in place (same chain, new event) is announced as new, and a Tornado Warning downgraded in
  place posts "… ended" first

The first look after a start is the baseline, so a restart never re-announces what was already
true. Fill and temperature levels clear 3 points below their threshold, so a value sitting on a
line doesn't flap. Off/asleep GPU machines and Tailscale devices coming and going are not
announced. Thresholds are constants at the top of `events.go`.

To add a section (ADS-B, weather, Meshtastic…), implement `Collector` (`main.go`) and register it in
`serve()`. The device ignores sections and keys it doesn't know, so the protocol doesn't change.

## Build and test

Needs only Docker. From this directory:

```sh
sh dist.sh          # go vet + go test, then dist/deskpanel-linux-amd64 and dist/deskpanel-windows-amd64.exe
```

`go test` covers the nvidia-smi parser (driver 550 and 610 XML), stale handling, truncation, and the
worst-case payload size (currently 6.4 KB against an 8 KB budget).

The screen editor page (`editor/`: plain ES modules, no build step) tests its layout logic on its own: where a
card fits, swaps, tab rename and delete, and the `layout.json` a save sends (`editor/model.test.mjs`, against
`editor/demo-data.json`).

```sh
sh editor/test.sh   # node --test in the node:22-alpine image, the folder mounted read-only
```

## Deploy the aggregator

```sh
cp .env.example .env   # then fill it in, see below
docker compose up -d --build
docker compose logs -f
```

`compose.yaml` uses host networking, so health checks can reach services running directly on the
host (a host firewall like ufw drops them from a Docker bridge subnet). `LISTEN` binds the LAN
address only, so the API stays off the tailnet. `/var/run/tailscale` is mounted read-only for the
LocalAPI socket.

**No build:** `compose.image.yaml` is the same service with the prebuilt image,
`ghcr.io/zebadrabbit/deskpanel-aggregator` (linux/amd64 and linux/arm64, built by `.github/workflows/image.yml`):
`docker compose -f compose.image.yaml up -d`; to update, `docker compose -f compose.image.yaml pull` and `up -d`
again. Tags: `latest` (main), `v*` (releases), `sha-<commit>`.

## Deploy the GPU agents

**Windows (workstation).** Elevated PowerShell can't see mapped drives, so copy
`dist/deskpanel-windows-amd64.exe` and `deploy/install-agent.ps1` to a local folder and run
`install-agent.ps1 -Aggregator <server-ip>` elevated. It installs to `C:\Program Files\deskpanel`, allows only the aggregator's IP through the
firewall on 9835, and registers a startup task that runs as SYSTEM and restarts on failure. Re-run it
to upgrade (an agent from before the fan field shows "Fan --" until then).

**TrueNAS SCALE.** Copy `dist/deskpanel-linux-amd64` to `/mnt/<pool>/deskpanel/deskpanel`
(`chmod 755`), then install `deploy/truenas-agent.yaml` as a Custom App (Apps → Discover Apps →
Custom App → Install via YAML, or the `midclt` line in the file). The NVIDIA runtime injects
`nvidia-smi` into the stock image. To upgrade, replace the binary and restart the app. An agent from before the fan field shows "Fan --" until it is upgraded.

## Config (`.env`)

| Variable | |
|----------|-|
| `PANEL_TOKEN` | bearer token the device sends. `openssl rand -hex 24` |
| `NOTIFY_TOKEN` | bearer token for senders of panel notifications (`POST /api/v1/notify`). Separate so a notifier can't mark cards done. Empty = notifications off |
| `EDITOR_TOKEN` | password for the screen editor at `/editor/`. At least 12 characters and different from `PANEL_TOKEN` and `NOTIFY_TOKEN`, or the editor stays off (`openssl rand -hex 16`). Empty = off |
| `LISTEN` | `<LAN address>:8088`. Bind the LAN address, not `:8088`, to keep the API off the tailnet |
| `TRUENAS_URL` | `wss://<nas>/api/current`. Must be wss: TrueNAS revokes an API key it sees over plain HTTP |
| `TRUENAS_USER`, `TRUENAS_API_KEY` | a user in the Read-Only Administrator group, and its key |
| `TRUENAS_CERT_SHA256` | the NAS cert fingerprint: `echo \| openssl s_client -connect NAS:443 2>/dev/null \| openssl x509 -noout -fingerprint -sha256`. Update it when the cert is renewed; the log says so |
| `GPU_AGENTS` | `name=http://host:9835,…` |
| `TAILSCALE_SOCKET` | `/var/run/tailscale/tailscaled.sock` |
| `SERVICES` | `name=http://host:port/health,…` (max 6), shown as up/down dots next to Tailscale |
| `PLANKA_URL`, `PLANKA_USER`, `PLANKA_PASSWORD` | the `deskpanel` Planka user. **Boards it is a member of are the boards on the panel** (up to 6, by name). Give it editor membership so Done can move cards |
| `WEATHER_LAT`, `WEATHER_LON` | decimal degrees the radar centres on and alerts are looked up for (US only), and the astronomical clock's location. Latitude -85 to 85, longitude -180 to 180; anything else stops the aggregator with a message. **Empty `WEATHER_LAT` turns weather and the clock's sky off** (their routes answer 404) |
| `WEATHER_ZOOM` | radar zoom level, 2 to 19, default `8`; anything else stops the aggregator with a message |
| `WEATHER_UA` | `User-Agent` for NWS and the tile servers; NWS asks for contact info in it. Default `deskpanel` |
| `WEATHER_CACHE` | directory for cached base-map tiles; compose sets `/data/weather` (the `notes` volume). Empty = no disk cache |
| `TZ` | time zone for the times in alert notes ("until 3:30 PM") and the clock's sunrise, sunset and day/night ring, e.g. `America/Chicago`. Unset = UTC. Keep it the same zone as the panel's `DESKPANEL_TZ`. Reaches the container through `env_file`; the binary embeds the zone database |
| `LAYOUT_FILE` | the hand-edited tab layout. Default `/config/layout.json`; compose mounts `./config` at `/config`, writable so the screen editor can save (the directory, not the file, so editors that save by renaming are still seen). Missing = the generated default. See [Layout](#layout) |
| `PROMETHEUS_URL` | base URL of a Prometheus (`http://host:9090`, no auth) for the general cards. Unset = those cards read "—" with `set PROMETHEUS_URL`. See [`GET /api/v1/cards`](#get-apiv1cards) |
| `CARD_<NAME>` | one whole HTTP header line, `Name: value`, for a JSON card that names it as `header_env` (only `CARD_` then `A-Z`, `0-9`, `_`, at most 40 characters after the prefix; a layout naming any other variable is refused, so `PANEL_TOKEN` and the TrueNAS key can never be sent to a URL). See [the JSON source](#the-json-source) |
| `HA_URL` | Home Assistant's base URL for `{"ha": …}` values, `http(s)://host:8123`. Compose uses host networking, so on the same server it is `http://127.0.0.1:8123`. Unset = `set HA_URL`. See [the Home Assistant source](#the-home-assistant-source) |
| `HA_TOKEN` | a Home Assistant long-lived access token, sent only to `HA_URL`. Unset = `set HA_TOKEN` |
| `DEMO`, `DEMO_WARNING` | `DEMO=1` fake data everywhere; add `DEMO_WARNING=1` for the demo Tornado Warning over home. `1`/`true`/`yes`/`on` turns a flag on, `0`/`false`/`no`/`off`/empty turns it off (`yes`/`no`/`on`/`off` in any case), anything else stops the aggregator |

Leave a collector's URL empty to turn it off.

## API

All but `/healthz` need `Authorization: Bearer $PANEL_TOKEN`.

```sh
T=$(grep ^PANEL_TOKEN= .env | cut -d= -f2); A=http://<server-ip>:8088

curl $A/healthz                                                    # ok
curl -i -H "Authorization: Bearer $T" $A/api/v1/panel              # 200 + ETag
curl -i -H "Authorization: Bearer $T" -H 'If-None-Match: "<etag>"' $A/api/v1/panel   # 304 if unchanged
curl -i -X POST -H "Authorization: Bearer $T" $A/api/v1/planka/cards/<card id>/done  # 204
```

### Notifications

Anything on the LAN with `NOTIFY_TOKEN` can put a message on the panel. It pops up with an **OK**
button, falls back to an unread bell in the status bar after 30 s, and stays in the panel's
history (tap the bell). Read state is kept here, so a panel reboot doesn't re-announce old notes.
The aggregator keeps the last 20 and saves them to `NOTES_FILE` on every change. `compose.yaml`
puts that in the `notes` volume, so they survive restarts. Without `NOTES_FILE` they're memory-only.

```sh
N=$(grep ^NOTIFY_TOKEN= .env | cut -d= -f2)
curl -X POST -H "Authorization: Bearer $N" -d '{"src":"Claude","msg":"Claude wants your attention"}' $A/api/v1/notify
# -> {"id":1794967001}. Optional "lvl": "info" (default) | "warn" | "alert". msg is cut at 88 bytes.
```

**ntfy-compatible publishing** (with `NOTIFY_TOKEN`), so anything that can notify an ntfy server can
notify the panel:
- `POST` or `PUT /<topic>` with the message as the body. `Title` / `Priority` headers (or `X-Title`
  / `X-Priority`) are honoured.
- Or `POST /` with JSON `{"topic","message","title","priority"}`.

The topic becomes the note's source, the title is prefixed to the message, and priority 5 is red,
4 amber, the rest blue. Responses are ntfy-shaped (`{"id","time","event":"message",...}`).

**Alertmanager / Grafana webhooks**: `POST /api/v1/alertmanager` (with `NOTIFY_TOKEN`) takes an
Alertmanager webhook payload; Grafana's webhook contact point sends the same shape.
- Each alert becomes a note: source = `alertname`, text = `summary` (or `description`) plus
  `instance`.
- Firing is amber, or red for `severity` critical/page/error; resolved is blue.
- At most 5 per delivery.

**Claude Code → panel**: hooks that put Claude on the panel while a fullscreen app hides the taskbar.
All run `async`, so they never slow Claude down.

| Hook | Posts |
|------|-------|
| `Notification` | `<project>: <message>` (permission prompts amber, "waiting for your input" blue) |
| `UserPromptSubmit` + `Stop` | `<project>: Claude finished (14 min)`, only for turns of 2 min or more |

- Windows: `deploy/claude-notify.ps1 -Event notify|start|stop`. Install steps are in its header.
- Linux/macOS: `sh deploy/install-claude-hooks.sh` installs `claude-notify.sh`, copies `NOTIFY_TOKEN`
  from `aggregator/.env` to `~/.claude/deskpanel-notify-token` (mode 600), and merges the three hooks
  into `~/.claude/settings.json` (backup in `settings.json.bak`; re-runs replace rather than
  duplicate). Then `/hooks` once, or restart Claude Code. The 2-minute threshold is
  `DESKPANEL_LONG_TURN_SEC` / `-LongTurnSec`.

The panel itself calls `POST /api/v1/notify/{id}/read` (with `PANEL_TOKEN`) when you tap OK or
open the history; it marks every note up to that id as read.

`POST …/done` moves the card to the top of its board's done list: the first list of type `closed`,
else a list named "Done". It returns 204 (also when the card is already there), 400 for a malformed
id, 404 for an unknown card, 409 if the board has no done list, 502 if Planka fails.

### `GET /api/v1/panel`

Live output, trimmed:

```json
{
  "homelab": {
    "updated_at": 1794964599, "stale": false,
    "nas": {
      "pools": [
        {"n": "tank", "h": "ONLINE", "ok": true, "used": 5464, "avail": 1847},
        {"n": "boot-pool", "h": "DEGRADED", "ok": false, "used": 22, "avail": 128}
      ],
      "alerts": [], "info": 3
    },
    "gpu": [
      {"host": "workstation", "n": "RTX 5060 Ti", "up": true, "temp": 49, "util": 12, "vram": 2429, "vram_t": 16311, "pwr": 17, "procs": 0, "st": "idle"},
      {"host": "truenas", "n": "RTX 2070 SUPER", "up": true, "temp": 47, "util": 0, "vram": 4471, "vram_t": 8192, "pwr": 12, "procs": 1, "st": "loaded"}
    ],
    "ts": [{"n": "server", "on": true}]
  },
  "planka": {
    "updated_at": 1794964599, "stale": false,
    "boards": [
      {"id": "1910683732176537016", "n": "Deskpanel", "can_done": true, "cards": [
        {"id": "1910683736010130884", "n": "Decide: which board the panel shows + done list name", "l": "Done", "d": true,
         "lb": [{"n": "Needs Panda", "c": 15569443}]}
      ]}
    ]
  }
}
```

| Field | Meaning |
|-------|---------|
| `updated_at` | oldest last-success time in the section, unix seconds (0 until every collector has succeeded once) |
| `stale` | a collector in this section failed its last run; its data is the last good copy |
| `nas.pools[]` | `n` name, `h` ZFS health, `ok` healthy, `used`/`avail` GiB (root dataset; boot pool is raw). The boot pool is last |
| `nas.alerts[]` | undismissed alerts, most severe first, INFO/NOTICE last (max 8): `lvl`, `msg` (up to 160 bytes) |
| `nas.info` | how many of those are INFO/NOTICE |
| `gpu[]` | `temp` °C, `util` %, `vram`/`vram_t` MiB, `pwr` W, `procs` compute (type C) processes, `st` `idle` / `loaded` (compute process, <5% util) / `busy` / `off` (agent unreachable). The fan is not here (8 KB budget), it travels in `/api/v1/cards` |
| `ts[]` | tailnet nodes incl. this one, by name (max 16): `n`, `on` |
| `svc[]` | `SERVICES` health checks in config order (max 6): `n`, `on` |
| `notify.notes[]` | newest 5 notes, newest first: `id` (rising, clock-seeded), `t` unix seconds, `src`, `msg`, `lvl`, `r` read |
| `planka.boards[]` | `n` name, `can_done` has a done list, `cards` the 3 most recently updated (comments count) |
| `cards[]` | `n` name, `l` list, `due` unix seconds (omitted if none), `d` already done, `lb` up to 2 labels with `c` colour `0xRRGGBB` |

Strings are cut on UTF-8 boundaries with `...` to keep the document under 8 KB.

The panel adds an `X-Panel-Boot: <boot> <reason> <prev_up_s>` header to this request, for example
`12 task_wdt 273600`: the boot number (counted from the last power-on), why it restarted, and how many
seconds the previous run lasted (0: unknown). The reasons are `poweron`, `ext`, `sw`, `panic`, `int_wdt`,
`task_wdt`, `wdt`, `deepsleep`, `brownout`, `sdio`, `usb`, `jtag`, `efuse`, `pwr_glitch`, `cpu_lockup` and
`unknown`. Every new header is logged (`panel: boot 12 after task_wdt, previous run up 3 d 4 h`). An
abnormal one (`panic`, the watchdogs, `cpu_lockup`, `brownout`, `pwr_glitch`, `efuse`, `sdio`, `unknown`)
also posts an amber bell note, "Panel restarted after a crash (task watchdog), up 3 d 4 h before".
`poweron`, `ext`, `sw`, `usb`, `jtag` and `deepsleep` are logged only. The first header after the
aggregator starts is a silent baseline, so restarting the aggregator never repeats a note. The header
doesn't change the document.

### `GET /api/v1/cards`

The finished contents of the layout's general cards (`number`, `gauge`, `bar`, `graph`, `status`) and the
history of GPU cards drawn as a graph. Same auth, ETag and 304 as `/panel`. It answers at once from cached
values, whatever Prometheus is doing; the panel polls it every 5 s. `/panel` is unchanged.

```json
{"cards": {"cpu":  {"st": "warn", "v": "87 %", "g": ["…60 chars…"], "th": 50},
           "ram":  {"v": "12 %", "f": 0.121, "sub": "9.2 / 76 GB"},
           "net":  {"v": "0.03 Mb/s", "v2": "0.04 Mb/s", "g": ["…", "…"]},
           "gpu-workstation": {"g": ["…"]}}}
```

Keys are card ids. Empty fields are left out.

| Field | Meaning |
|-------|---------|
| `st` | `ok` (the default, left out), `warn`, `bad`, `stale` (a query failed after a good value; the old value is kept) or `none` (no value to show: the card draws "—" and `sub` says why) |
| `v`, `v2` | the main value, and a second line's for a two-value graph. Formatted text, at most 15 bytes |
| `sub` | the sub-line, or the reason when `st` is `none`, at most 23 bytes |
| `f` | gauge and bar fill, 0..1 with 3 decimals |
| `g` | graph lines, one or two strings. **One base64url character per point** (`A-Z a-z 0-9 - _` = 0..63), at most 60 per line, **oldest first**, scaled between the card's min and max. A short series is drawn right-aligned |
| `th` | the `warn` threshold on the same 0..63 scale, for the dashed line |

A GPU card whose slots use `fan` (the gauge, line 1 or line 2) appears with `v` (`"45%"`, or `"--"` when unknown: an old agent, N/A, or the GPU offline) and `f` (0.45, left out when unknown); `0%` is a reading and has `f` 0. Line 1 as `fan` graph adds `g`, the fan history.

A GPU card with `line1.as` of `graph` appears with just `g` (its text still comes from `/panel`): an hour of
once-a-minute samples that the aggregator keeps **in memory**, so they restart empty. A GPU that was off for
over an hour starts a fresh line.

The document stays under **16 KB** (16384 bytes, the panel's buffer is 20 KB) even for the largest layout
(144 cards at every field cap; `TestCardsUnder16KB`). Prometheus (`PROMETHEUS_URL`) is read with no auth:
instant queries every 30 s, graph ranges (the last hour, 60 s step) every 60 s, 5 s timeout per query. A failed query
keeps its last value, marked `stale`; one that never answered is `none` with `unreachable`. Prometheus answering 400 or 422 is
`none` with `bad query`. A query returning several series is `none` with `add @instance (N)` (a shortcut with no
instance) or `returns N series`; no answer or NaN is `no data`.

```sh
curl -H "Authorization: Bearer $T" $A/api/v1/cards      # {"cards":{}} until a layout uses general cards
go run . demo-cards /tmp/cards                          # writes /tmp/cards/cards.json (needs Go; sim/run.sh does it in Docker)
```

`deskpanel demo-cards <dir>` writes `<dir>/cards.json`: one `/api/v1/cards` document with fixed numbers for
every card of every demo layout (`demo-layout`'s), so the simulator's screenshots are reproducible. It needs
no Prometheus.

### The JSON source

A general card's `value` can be `{"json": {"url": "http://app.lan/api/status", "path": "queue.depth", "every": 30, "header_env": "CARD_APP_TOKEN"}}` (`jsonsrc.go`, `values.go`). One collector polls each distinct source (same url, path and header = one fetch) on its own schedule. Limits:

- `url`: http or https, at most 256 bytes. `path`: dotted keys, a number indexes an array, at most 128 bytes.
- `every`: 10 to 3600 s, default 30. `header_env`: optional, a `CARD_*` name (above); its value must contain a `:` and a name before it, else the card says `bad header`. An unset variable is a layout error.
- 5 s per fetch, a reply of at most 256 KB (`reply too big`), 2xx only, at most 4 fetches at once, at most 64 distinct sources per layout. A slow source is skipped, not queued; the cards document never waits on one.
- A redirect to another host, or from https down to http, is refused (so a header never follows it); it reads as `unreachable`.
- Graphs: one sample a minute, the last 60, the last hour, in memory (a restart starts them empty). A source that turns between number and text drops its history.
- Reasons (`sub`, with `st` `none`): `no <last key>` (path missing, or null/object/array/empty text there), `unreachable` (no answer, HTTP error or bad JSON before any good value), `reply too big`, `bad header`, `not a number` (text on a gauge, bar or graph). After a good value, an unreachable source goes `stale` and keeps the old value; the other reasons replace it.
- Numbers use the card's `unit` and `min`/`max`; `true`/`false` read as 1/0; text shows on a `number` (cut to 15 bytes) and drives a `status` (`ok`, `up`, `true`, `healthy`, `running`, `online`, `active`, `on`, `home`, `open`, `playing`, any case, are up; anything else is down, with the text as its word).

### The Home Assistant source

A general card's `value` can be `{"ha": "sensor.living_room_temperature", "attr": "brightness", "every": 15}` (`ha.go`, `values.go`). It's the JSON source's collector, scheduler, 4-fetch pool, history and 64-source limit; the key is `ha:<entity>\x00<attr>`. One fetch per distinct entity + attr: `GET HA_URL/api/states/<entity>` with `Authorization: Bearer HA_TOKEN`. Per entity, not one batch `GET /api/states`: that needs no new scheduler, history or stale code, and a few dozen ~1 KB replies cost less than re-reading every entity in the house (often hundreds of KB) each interval.

- `ha`: an entity id, `^[a-z0-9_]+\.[a-z0-9_]+$`, at most 255 bytes, checked at parse time and again before each request, so nothing else can reach the URL the token goes to. `attr`: optional, at most 64 bytes. `every`: 10 to 3600 s, default 15. Unknown keys are refused. A Prometheus query starting `ha:` or `json:` is refused at parse time.
- `HA_URL` must be `http(s)://host…`, else the card says `set HA_URL`; an unset `HA_TOKEN` says `set HA_TOKEN`. The token comes only from the environment and goes only to `HA_URL`.
- 5 s per fetch, a reply of at most 256 KB, 2xx only. Every redirect is refused (`unreachable`), so the token never leaves `HA_URL`.
- A 401 reads `bad token` and a 403 `forbidden` (HA's answer once it has banned the address). Either latches that token: no further request until `HA_TOKEN` changes, which in production means recreating the container, because HA bans an address after repeated failed logins. While HA hasn't yet accepted a token, one request goes at a time (one probe), so a wrong token costs one failed login, not one per pool slot.
- Other reasons (`sub`, with `st` `none`): `no entity` (404), `bad entity` (the id isn't `domain.object_id`), `no attr <name>` (missing, empty, list or object; an attribute that is present but null, like a light's brightness while it is off, shows the entity's state instead, e.g. `off`), `reply too big`, `unreachable` (no answer, HTTP error, bad JSON, or a redirect, which is refused so the token can't follow it, before any good value), `not a number` (text on a gauge, bar or graph). On a gauge, bar or graph, HA's `unavailable` or `unknown` shows as that word instead of `not a number`. After a good value, an unreachable source goes `stale` and keeps the old value.
- A state that parses as a finite number is a number (HA's states are text); `true`/`false` read as 1/0; other text shows on a `number` (cut to 15 bytes) and drives a `status` (up-words as for JSON, anything else down with its word).
- Unit: the card's `unit`, else the entity's `unit_of_measurement` (state only; an attribute has no HA unit). `°C`, `%` and `W` are formatted as for any card, and a gauge or graph with `%` or `°C` scales 0 to 100.
- Graphs: as for JSON, except a switch between a number and text keeps the history (HA says `unavailable` while it restarts). `DEMO=1` answers `ha:` keys from `demoHA` (`demo.go`), and the `ha-demo` layout shows them.

## Layout

The tabs on the panel come from `GET /api/v1/layout`, which serves `LAYOUT_FILE` when it is valid and
otherwise a default generated from what the collectors found (`layout_default.go`: a gpu card per GPU, up
to 4; a pool card per pool, up to 7; NAS alerts; dots; then the Planka and Weather tabs). The owner's guide
to the file format is "Make my own tab" in [docs/layout.md](../docs/layout.md#make-my-own-tab); the rules are in
`layout.go` (`parseLayout`), and `layout.example.json` is a sample.

```sh
docker compose run --rm -T deskpanel layout-default > config/layout.json   # -T: no TTY, or messages and CRLF end up in the file
go run . demo-layout /tmp/layouts                                       # the simulator's layouts (needs Go; sim/run.sh does it in Docker)
curl -i -H "Authorization: Bearer $T" $A/api/v1/layout                     # 200 + ETag
curl -i -H "Authorization: Bearer $T" -H 'If-None-Match: "<etag>"' $A/api/v1/layout   # 304 if unchanged
```

- **Response.** `{"tabs":[{"name","cards":[{"id","type","at":[col,row],"size":[w,h],"src","title","tap"}]}]}`.
  `size` is `[w, h]` here (it is `"WxH"` in the file), `tap` is always present (the type's default when the
  file gives none: `alert-details` for `nas-alerts`, else `none`), and `src` / `title` appear only when set.
  A pool card's `src` is cut to 24 bytes the way the TrueNAS collector cuts pool names, so a long name in the
  file still matches. The ETag is a hash of the document, `Cache-Control: no-cache`.
- **`tour`.** An optional root key of the file, `{"on","idle_min" 1-120,"clock_s" 5-600,"tab_s" 5-600,"clock","skip":[tab names]}`,
  every part optional. The response always carries it complete: `{"on","idle_s","clock_s","tab_s","clock","skip"}`, idle in seconds and `skip` as 0-based tab indexes. Firmware
  without the feature ignores it (unknown root key) and keeps 5 min / 60 s / 20 s.
- **503 "layout not ready yet"** until the first layout exists after a restart. With a `layout.json` that is
  immediate. Without one the default waits for the GPU and TrueNAS collectors' first runs (each times out at
  20 s), at most 25 s. The panel treats a 503 as "keep what's shown", so a restart never flashes a short
  default. 404 is a different matter: it is what an aggregator without layouts answers, and the panel then
  shows "Update the aggregator to use layouts".
- **Reload.** The file is looked at every 5 s (mtime and size). A new version is parsed once it is unchanged
  on two looks in a row, so a half-written save is never read (the very first load after a start doesn't
  wait). An empty file is never parsed. A file that is missing keeps the last file layout for 3 looks
  (renaming and delete-then-create saves), then the default takes over.
- **A bad file** posts one panel note (`layout.json line N: <what>`, cut to 88 bytes) and logs it, once per
  bad version, and the last good layout stays served. Rules checked: UTF-8 (a BOM is stripped, UTF-16 is
  "save as UTF-8"); 1-6 tabs with unique, non-empty names of at most 15 bytes; at most 24 cards per tab;
  card id at most 23 bytes and unique across the file; `src` and `title` at most 27 bytes, `tap` 23; a known
  `type` and one of its sizes (`gpu` 2x3 2x2 1x3 1x2 1x1, `pool` 1x2 1x1 2x2, `nas-alerts` 1x2 1x1, `dots`
  4x1 2x1 4x2, `board` and `radar` 4x6); inside the 4 x 6 grid and not overlapping; `dots` `src` one of
  `tailscale`, `services`, `both`; `tap` one of `notifications`, `alert-details`, `none`, `tab:<an existing
  tab>`, and none (or `none`) on `board` and `radar`; a 4x6 card alone on its tab.
  General cards add: sizes `number` 1x1 1x2 2x2, `gauge` 1x2 2x3, `bar` 1x1 1x2 2x1, `graph` 2x2 2x3 4x2
  4x3, `status` 1x1 2x1; a required `value` (a shortcut, `{"prometheus": "<PromQL, up to 512 bytes>"}`, or for a
  graph a list of two) and no `src`; `unit` up to 8 bytes; `warn` / `bad` in order (reversed with `below`) and
  none on `status`; `min` below `max`, and none on a gauge or bar with a shortcut. Other types take none of
  `value`, `unit`, `warn`, `bad`, `min`, `max`, `below`. `gpu` alone takes `slots`
  (`gauge`, `line1` `{value, as}`, `line2`). The user guide has the details.

### Screen editor routes

With `EDITOR_TOKEN` set (12+ bytes; otherwise every route below answers 404), `GET /editor/` serves the page (no
auth, CSP `default-src 'self'`). The API needs `Authorization: Bearer $EDITOR_TOKEN` (a wrong token waits 1 s, then 401);
`config/` must be mounted without `:ro`.

**What the editor password gives away.** Whoever has `EDITOR_TOKEN` can see every Home Assistant entity's name and
state, read any entity's attribute values through Test, see Prometheus's metric names, the Planka card titles and the
notification history (the preview carries the whole panel document), and the names (not the values) of your `CARD_*`
variables (never their values). They can also rewrite the tabs, and point a card, or the Test button, at any address
the server can reach, and the server then sends that card's `CARD_*` header there, exactly as it does for someone who
can edit `layout.json`. So the editor password is as powerful as write access to the file: a long random string of
its own, never reused (the editor stays off if it equals `PANEL_TOKEN` or `NOTIFY_TOKEN`).

- `GET /api/v1/editor/ping` : 204, the page's unlock check.
- `GET /api/v1/editor/layout` : `{"layout": <layout.json form>, "from_file", "error"}` and an `ETag` (`"none"` when there is no file).
- `PUT /api/v1/editor/layout` : `If-Match` = that ETag, body at most 256 KB. 412 when the file changed on disk, 422 `{message, tab, card}`
  when `parseLayout` refuses it, 500 `can't write ...` when the folder is read-only. A good save keeps the old file as `layout.json.bak`.
- `GET /api/v1/editor/sources` : always 200, within 5 s, cached 30 s. The tables `parseLayout` reads (`limits`, `types` with
  their sizes, the default first, `shortcuts` with the types each suits and whether it needs `@instance`, GPU `slots` and
  their `default`, `tour` defaults, `taps`, `units`, `dots`), the `gpus` and `pools` the collectors report, `prometheus`
  (`instances`, `mounts`, `metrics`, from its label API) and `ha` (`entities` from `/api/states`, through the cards' client:
  never asked while its token is refused or unset), each with `configured`, `needs` (the `.env` settings still to add,
  by name), a short `error` and `truncated` (a list hit its cap: 5000 metrics, keeping `up` and `node_*`; 2000 entities,
  keeping `sensor.*`, then `binary_sensor.*`), and `card_env`: the `CARD_*` names set in `.env`, never their values.
  Prometheus and Home Assistant are asked only when this is called; an answer saying HA is busy isn't cached.
- `POST /api/v1/editor/test` : a general card's `type`, `value`, `unit`, `min`, `max`, `warn`, `bad`, `below` as in
  `layout.json`, checked as `parseLayout` checks them; the value is asked of its source once (a graph's plot too) and
  the card answered as `/api/v1/cards` serves it (`st`, `v`, `v2`, `f`, `sub`, `g`, `th`), or `{"error"}` (the
  card's own reason, or `parseLayout`'s rule). One at a time and one a second, else 429.
- `GET /api/v1/editor/preview` : `{"panel", "cards"}`, the documents `/api/v1/panel` and `/api/v1/cards` serve now, byte for byte.

## Weather

Three more routes, same bearer auth (`PANEL_TOKEN`). They exist only when `WEATHER_LAT` is set (or in
demo mode); otherwise they answer 404, which the panel reads as "Weather isn't set up".

```sh
curl -i -H "Authorization: Bearer $T" $A/api/v1/weather                 # 200 + ETag; 304 with If-None-Match
curl -H "Authorization: Bearer $T" $A/api/v1/weather/frame/<t> -o f.z   # one radar frame
curl -H "Authorization: Bearer $T" $A/api/v1/weather/alert/0            # full text of alerts.list[0]
```

| Route | Returns |
|-------|---------|
| `GET /api/v1/weather` | the Weather document: `updated_at`, `stale`, `now` (conditions: `t` °F, `rh`, `code`, `day`, `text`, `hi`, `lo`, `wind`), `frames` (the valid times, unix seconds, oldest first; 5, 300 s apart), `alerts` (`list`: up to 4 alerts at home, takeover first, each `n`, `key`, `event`, `cls`, `until`, `take`, `lvl`; `polys`: up to 8 warning outlines in frame pixels, `k` `t`/`s`, `h` over home). Own ETag, kept apart from `/api/v1/panel` to protect its 8 KB budget |
| `GET /api/v1/weather/frame/{t}` | radar frame `t` (a time from `frames`): 800×368 little-endian RGB565, zlib-compressed, base map already in. 400 for a non-number, 404 if not held. Cacheable for an hour |
| `GET /api/v1/weather/alert/{n}` | `{key, event, headline, sent, expires, desc, instr}` for `alerts.list[n]` (n = 0-3) from the last poll; `key` is that list entry's key, which the panel checks so a chip never shows another alert's text. Whitespace-normalised, at most 3 KB in all. 404 when there's no such alert |

How alerts are chosen: `status=actual` only (test and exercise messages never show), a cancelled
warning drops out of the list, and an update replaces the message it refers to. `lvl` is the tornado
escalation: 1 observed, 2 considerable damage threat, 3 catastrophic (emergency). A feature the
parser can't read is skipped and logged, **unless** its event is a Tornado or Extreme Wind Warning, or its
event is missing or broken and the feature mentions one, in which
case the whole poll fails so the previous takeover stays up (and "Can't reach NWS" can fire after 10
minutes) rather than quietly vanishing. Outlines from a failed in-view fetch are kept for at most 10
minutes. The in-view fetch asks only for the states the map touches (`area=`), and is nationwide when it
touches none. It has its own 5 s timeout so it can't delay the home alerts.

Radar: `WEATHER_ZOOM` picks the tile zoom; the base map is fetched once and cached (in memory, and in
`WEATHER_CACHE` on disk), and each poll adds any new IEM frame, keeping five 5 minutes apart.

## Astronomical clock

Two routes for the panel's full-screen clock (`astroapi.go`), same bearer auth (`PANEL_TOKEN`) and ETag/304 as
`/panel`. Like the weather routes they need `WEATHER_LAT` (or demo mode) and answer 404 without it; the panel
then shows "Set WEATHER_LAT/LON for the sky" and asks again every 10 minutes.

```sh
curl -i -H "Authorization: Bearer $T" $A/api/v1/astro                   # 200 + ETag; 304 with If-None-Match
curl -H "Authorization: Bearer $T" $A/api/v1/astro/sky/<sky> -o sky.z   # the picture the document's "sky" names
go run . demo-astro /tmp/astro                                         # the simulator's files (needs Go; sim/run.sh does it in Docker)
```

```json
{"sky":1795034700,"rise":"7:23 AM","set":"4:07 PM","daylight":"8 h 44 min","moonrise":"1:26 PM","moonset":"—",
 "phase":"Waxing gibbous","illum":0.634,"waxing":true,"day":[443,967],"label":"OVERHEAD NOW",
 "overhead":["Cassiopeia","Andromeda","Perseus","Cepheus","Lacerta","Pisces","Pegasus","Triangulum"],"stale":false}
```

| Field | Meaning |
|-------|---------|
| `sky` | unix time of the current sky picture (0 before the first one); fetch it as `/api/v1/astro/sky/{sky}` |
| `rise`, `set`, `moonrise`, `moonset` | clock times in `TZ`, for the local day it is now. `"—"` when it doesn't happen that day (polar day or night; about once a month for the moon) |
| `daylight` | sunrise to sunset, `"8 h 44 min"`; `"—"` without both |
| `phase` | `New moon`, `Waxing crescent`, `First quarter`, `Waxing gibbous`, `Full moon`, `Waning gibbous`, `Last quarter` or `Waning crescent` |
| `illum`, `waxing` | the moon's lit fraction (0..1, 3 decimals), and whether it is growing |
| `day` | `[sunrise, sunset]` as minutes into the local day, for the dial's day/night ring; `[-1, -1]` without both |
| `label` | `OVERHEAD NOW`, or `TONIGHT AT 9 PM` while the sun is up |
| `overhead` | constellations whose stick figure has a star more than 40° up, highest first: now, or at 9 PM local tonight while `label` says so. At most 12 names of at most 23 bytes |
| `stale` | the last sky render failed; the previous picture is still served |

`GET /api/v1/astro/sky/{t}` is the sky disc as 378 × 378 little-endian RGB565, zlib-compressed (the radar
frames' format), about 280 KB raw. Zenith at the centre, the horizon at radius 189 (azimuthal equidistant),
north up, east on the left as you look up. Everything outside the disc is the dial colour `#040814`. 404 unless
`t` is the current picture's time (a new one replaces it every 2 minutes) or for a non-number.

How it's made (`astro.go`, `sky.go`):
- **Astronomy**: low-precision formulas, good to about a degree for the moon and better for the sun; rise and
  set to about a minute (sun at −0.833°, moon at +0.125°, found by a 1-minute scan of the local day). The moon's
  phase comes from its angle to the sun.
- **The picture** is drawn with Go's `image` package only, off the request path: a goroutine renders it every
  2 minutes and the document is recomputed every minute; a request only copies the last result. A failed render
  keeps the old picture and sets `stale`. Stars to magnitude 4.5 sized by brightness, all 88 constellation
  figures in gold, a procedural Milky Way along the galactic plane, the moon with its phase, and by day the
  sun. The background follows the sun's altitude: night colours below −12°, a lighter day blue above 0° (stars
  and figures softened), blended in between.
- **Data**: `skydata/stars.csv` and `skydata/figures.json` are trimmed from
  [d3-celestial](https://github.com/ofrohn/d3-celestial) by Olaf Frohn (BSD 3-Clause); stars from XHIP (an
  extended Hipparcos compilation), figures and names after the IAU. Sources, changes and the licence:
  `skydata/LICENSE`.
