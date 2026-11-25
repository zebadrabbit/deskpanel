# Troubleshooting

`<server-ip>`, `<repo>`, **the server** and **the PC** mean the same on every page: see [Words used in these pages](README.md#words-used-in-these-pages). [Back to the index](README.md).

A card showing "—" and a few small words is explained in [Cards](cards.md).

## When something looks wrong

| You see | Probably | Do |
|---------|----------|----|
| **Wi-Fi isn't set up yet** (firmware from the flasher page) | No usable `settings.txt` with a `WIFI_SSID`: missing, misnamed (`settings.txt.txt`, `settings (1).txt`), not in the card's top folder, or the card isn't FAT32 | Make the file on the [flasher page](https://zebadrabbit.github.io/deskpanel/), copy it to the card's top folder as exactly `settings.txt`, restart the panel. |
| **STALE** in the status bar | The aggregator is down, or the panel lost Wi-Fi | On the server: `cd <repo>/aggregator && docker compose ps && docker compose logs --tail 30`. An amber Wi-Fi icon means the panel can't join its network. Right after editing `settings.txt`, a wrong `AGGREGATOR_URL` or `PANEL_TOKEN` shows only as STALE: fix or remove those lines. The serial log names a refused line (`settings.txt line N: …`), or says `AGGREGATOR_URL isn't usable: not polling`. |
| A GPU tile says **OFF** | That machine is off or asleep, or its agent stopped | Windows: Task Scheduler → `deskpanel-agent`. TrueNAS: Apps → `deskpanel-agent`. |
| Homelab page dimmed, no STALE pill | A TrueNAS or Tailscale check is failing | Check the logs (above). If they say the TrueNAS certificate fingerprint changed (the cert was renewed), put the new value in `TRUENAS_CERT_SHA256` and recreate. |
| A **service** dot red | That service isn't answering its health URL | Check the service itself, then `curl` its URL from the server. |
| A **pool** tile red | ZFS reports the pool unhealthy, or it's nearly full | `zpool status <pool>` on the NAS. Once the cause is fixed, `zpool clear <pool>`. |
| Claude notifications stopped | The hook isn't loaded, or the token/URL is wrong | `/hooks` in Claude Code; check `~/.claude/deskpanel-notify-token` and `-url`; send a test with the curl in [Put my own message on the panel?](how-to.md#put-my-own-message-on-the-panel) |
| A board is missing on the Planka page | `deskpanel` isn't a member, or there are more than 6 boards | Add the member; only the first 6 boards, alphabetically, are shown. |
| Done says "HTTP 409" | That board has no Done list | Add a list named **Done**. |
| No saved copy after a reboot | No SD card, or one that isn't FAT | The boot log says why. Use a FAT32-formatted card; the panel never formats one itself. |
| Weather says **"Weather isn't set up on the aggregator"** | `WEATHER_LAT` is empty, or the aggregator is older than the firmware | Set `WEATHER_LAT` / `WEATHER_LON` in `.env` and rebuild (see [Set up the weather?](screen.md#set-up-the-weather)). |
| Radar time chip is **amber** | The newest radar frame is 15+ minutes old | Usually the radar service (Iowa State's) being late; it fixes itself. If it stays: `docker compose logs --tail 30` on the server, and check the server's internet. |
| The aggregator **exits at start** and the log says `… is outside …` or names `DEMO` | A `WEATHER_LAT`, `WEATHER_LON`, `WEATHER_ZOOM`, `DEMO` or `DEMO_WARNING` value in `.env` is out of range or not 1/0 (or yes/no) | Correct that value in `aggregator/.env`, then `docker compose up -d`. |
| **No outlines** on the map | No tornado or severe thunderstorm warnings are in view. That's the normal case. | Try [the demo warning](screen.md#see-what-a-tornado-warning-looks-like) to see them. |
| **No alert** when you expected one | NWS hasn't issued one for your exact spot (a warning covers a drawn area, not a whole county), the panel polls once a minute, or the alerts feed is down | Check `https://api.weather.gov/alerts/active?point=<lat>,<lon>` in a browser. A "Can't reach NWS" note in the bell means the feed is down. |
| Alert times are **hours off** | `TZ` isn't set in `.env`, so times show in UTC | Add `TZ=America/Chicago` (your zone) and recreate the container. |
| The clock says **"Set WEATHER_LAT/LON for the sky"** ([looks like this](screenshots/astro-nolocation.png)) | No location in `.env`, or the aggregator is older than the firmware | Set `WEATHER_LAT` / `WEATHER_LON` and `cd <repo>/aggregator && docker compose up -d --build`. The panel asks again every 10 minutes, so the sky can take that long to appear. |
| The clock's **sky is dim** | No word from the aggregator for 10+ minutes, or it failed to draw the sky | As for STALE above. `docker compose logs --tail 30` shows an `astro:` line if drawing failed. It brightens on the next good answer. |
| Sunrise and sunset are **hours off**, or the hour hand sits in the navy at midday | The aggregator's `TZ` and the panel's `DESKPANEL_TZ` aren't the same zone | Set both to your zone: `TZ` in `.env` (recreate the container), `DESKPANEL_TZ` in `settings.txt` (restart the panel) or `secrets.h` (build and flash). |
| **Amber Wi-Fi** right after editing `settings.txt` | A typo in `WIFI_SSID` / `WIFI_PASSWORD` | Fix or remove those lines. The serial log names a bad line (`settings.txt line N: …`). |
| The clock shows **"--:--" and no hands** | The panel doesn't know the time yet (no internet time, and the onboard clock lost it) | It fixes itself once Wi-Fi and internet time are up. |
| Moonrise or moonset says **"—"** | That day has none. It happens about once a month | Nothing to do. |
| The tour **doesn't start** | Something is holding it: a sheet, pop-up or tornado notice is open, the clock was opened by hand, or someone touched the screen in the last 5 minutes | Close what's open, or tap the clock away, and wait 5 minutes. |
| Panel frozen or rebooting | A firmware crash | Watch its log on the PC: `python -m serial.tools.miniterm --raw COMx 115200` (pyserial; PlatformIO's Python has it). `Ctrl+]` quits. See [Why did the panel restart?](how-to.md#why-did-the-panel-restart) |
| An amber bell note **"Panel restarted after a crash (task watchdog)"** | The screen or the network side stopped responding for 120 s, so the panel restarted itself | Nothing if it's rare. If it repeats, keep the serial log (it names the stuck task) and [open an issue](https://github.com/zebadrabbit/deskpanel/issues) with it. See [Why did the panel restart?](how-to.md#why-did-the-panel-restart) |
| A note "layout.json line N: …" | Your layout file has a mistake on that line. The panel keeps the last good layout | Fix the line named and save; the panel follows in about 20 s. Common ones: a size the card type doesn't have, two cards on the same squares, a `tap` naming a tab that isn't there, a name over 15 bytes. |
| The panel didn't change after you saved | The file wasn't picked up: it isn't at `aggregator/config/layout.json`, it's still the default, or the aggregator restarted | `ls <repo>/aggregator/config`, then `docker compose logs --tail 30` on the server (a bad file is logged as `layout /config/layout.json: layout.json line …`). A save is only read once it's unchanged on two looks, 5 s apart. |
| A card says **no gpu 'X'** / **no pool 'X'** | Its `src` matches no GPU agent or pool | Check the spelling against `GPU_AGENTS` in `.env` and the pool name in TrueNAS; for a second GPU on a host use `host#2`. |
| The layout file has odd characters, or "save as UTF-8" | The editor saved it as UTF-16 | Save it again as UTF-8 (in Notepad: Save As → Encoding → UTF-8). Better: regenerate with the `-T` command in [Make my own tab?](layout.md#make-my-own-tab) |
| `/editor/` says **not found** | `EDITOR_TOKEN` isn't set, is under 12 characters, or is the same as `PANEL_TOKEN` or `NOTIFY_TOKEN` | Set a password of its own, 12+ characters (`openssl rand -hex 16`), in `aggregator/.env`, then `cd <repo>/aggregator && docker compose up -d --force-recreate` (with the prebuilt image add `-f compose.image.yaml`). `docker compose logs --tail 30` has a line containing `editor off:` that says which. |
| Editor Save says **"can't write ... Mount config without :ro"** | An older `compose.yaml` still mounts `./config` read-only | In the compose file you use (`aggregator/compose.yaml`, or `compose.image.yaml`) the line must be `- ./config:/config` with no `:ro`. Then `docker compose up -d --force-recreate` (add `-f compose.image.yaml` for the prebuilt image). |
| Editor says **"Wrong password."** | The password isn't `EDITOR_TOKEN` as the container has it (it only reads `.env` when it is created) | Check the line in `aggregator/.env`; after changing it, recreate the container. A wrong try waits a second before it answers. |
| Editor Save refuses with **`layout.json changed on disk; reload`** and offers Reload | The file changed on disk after the page loaded it (a hand edit, or a save from another browser tab) | Press Reload to see the new file. To keep your version first, View JSON, then Download. |
| Save says **`too big for the panel (N bytes, at most M): use fewer cards`** | The whole layout is over the panel's 12 KB limit (a hundred or more small cards, or long names) | Remove cards, shorten names and ids, or move cards to another tab; the numbers in the message say how far over you are. |
| The tour ignores the Tour & clock settings | The panel's firmware is older than this feature | Flash it again (see [Update the panel's firmware?](how-to.md#update-the-panels-firmware)). Until then it keeps 5 min / 60 s / 20 s. |
| **The startup screen (the logo) stays** | It shows for about 3 s at power-on, then until a layout is on screen. Its line and three dots say how far it got: **Joining Wi-Fi…**, **Waiting for the aggregator…** (Wi-Fi is up, no answer yet), **Loading your layout…** (after an aggregator restart without a layout file this can last up to 25 s). Green dot = done, brass = working on it | Wait half a minute. Stuck on Wi-Fi: check the Wi-Fi name and password. Stuck on the aggregator: it is down (see STALE above). |
| **"Update the aggregator to use layouts"** | The aggregator is older than the firmware | `cd <repo>/aggregator && docker compose up -d --build`. |

## Where everything lives

| What | Where |
|------|-------|
| Aggregator | The server, `<repo>/aggregator`, Docker container `aggregator-deskpanel-1` on `http://<server-ip>:8088` (LAN only; it binds the address in `LISTEN`). Notes are kept in the `notes` Docker volume. |
| Secrets | `aggregator/.env` (all tokens, the TrueNAS key, the Planka password) and `firmware/main/secrets.h`. Both gitignored; templates are `.env.example` and `secrets.example.h`. And, if you use one, `settings.txt` on the panel's SD card (plain text). |
| GPU agents | Windows: Task Scheduler `deskpanel-agent`, files in `C:\Program Files\deskpanel`. TrueNAS: a Custom App `deskpanel-agent`, with the binary in a dataset of your choice. |
| Service accounts | A TrueNAS user (Read-Only Administrator) with an API key, and a Planka user `deskpanel` (board member = shown on the panel). |
| Claude hooks | `~/.claude/settings.json`, `~/.claude/hooks/claude-notify.*`, `~/.claude/deskpanel-notify-token` and `-url`, on each computer. |
| Your tabs | `aggregator/config/layout.json` on the server (gitignored; absent = the default layout). The screen editor keeps the file it replaced next to it as `layout.json.bak`. |
| The screen editor | `http://<server-ip>:8088/editor/`, switched on by `EDITOR_TOKEN` in `aggregator/.env`. The made-up-data demo is on the flasher site, at `editor/`. |
| On the panel's SD card | `panel.jsn`, the last good document (rewritten at most once a minute), and `layout.jsn`, the last layout. For the clock: `astro.jsn`, its column (at most once a minute), and `sky.raw`, the sky picture (at most every 6 hours). `boot.log`, one line per boot (at least 160 kept). `settings.txt`, your settings (read at boot, never written). |

Technical detail (protocol, config variables, build internals) is in [aggregator/README.md](../aggregator/README.md)
and [firmware/README.md](../firmware/README.md).
