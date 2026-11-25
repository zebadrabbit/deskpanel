# firmware

ESP-IDF 5.5 + LVGL 9 for the **Waveshare ESP32-S3-Touch-LCD-4.3B**. A thin client: it renders the
aggregator's `/api/v1/panel` document and sends touch actions back. No API keys or data shaping here.

| File | |
|------|-|
| `main/board.c` | I2C bus, CH422G expander, RGB panel, GT911 touch, PCF85063A RTC, SD card, LVGL port |
| `main/net.c` | Wi-Fi with reconnect backoff, SNTP, RTC fallback, the poller (`GET /api/v1/panel` every 5 s with `If-None-Match`, backing off to 30 s while the aggregator is down), card-done / note-read POSTs, and the SD copy. Never touches LVGL |
| `main/notify.c` | notification pop-up, unread bell, history |
| `main/panel.c` | JSON → fixed-size `panel_t` (cJSON, no IDF deps, host-tested) |
| `main/settings.c` | `settings.txt` → `settings_t`, `KEY=value` lines over `secrets.h` (no IDF deps, host-tested by `test/settings_test.c`) |
| `main/ui.c` | status bar (clock, Wi-Fi, stale), the layout's tabs, bottom tab bar, the idle tour and the clock button. A 500 ms timer try-locks the newest layout and `panel_t` from the net task, rebuilds the tabs when the layout changes and calls each card's `update()` |
| `main/astro_view.c` | the full-screen astronomical clock: the dial, hands, moon ring and column, drawn over the aggregator's sky picture |
| `main/astro.c` | `/api/v1/astro` JSON → fixed-size `astro_t` (host-tested by `test/astro_test.c`) |
| `main/card_*.c` | one card type each, a `card_type_t` with `create()` / `update()` / `destroy()` (see `card.h`); `card_board.c` is Planka, `card_radar.c` the radar |
| `main/takeover.c` | the tornado takeover (sticky notice, its OK, the jump to the radar's tab) and the weather alert sheet, whatever the layout holds |
| `main/fonts/` | Montserrat Medium TTF (SIL OFL 1.1, `OFL.txt`), embedded and rendered with LVGL's Tiny TTF for user text |
| `main/secrets.h` | Wi-Fi, aggregator URL and token, timezone; `settings.txt` on the SD card can override each at boot. Gitignored; copy `secrets.example.h` |

The card modules (`main/card_*.c`, all built on `card_util.c`), one `card_type_t` each:

| Module | Card type | |
|--------|-----------|-|
| `card_gpu.c` | `gpu` | a gauge, line 1 (bar, graph or text) and line 2, state pill, from 2x3 down to 1x1. The layout's `slots` choose the values (defaults: temperature, VRAM bar, util and power); a graph's points come from `/api/v1/cards`, the text from `/panel`. A graph becomes a bar on 1x2 and a 2x2 hides line 2; `power` has no bar, so it is text |
| `card_number.c` | `number` | title and value; a sub-line from 1x2 up (1x1: one row) |
| `card_gauge.c` | `gauge` | an arc (76 px on 1x2, 100 px on 2x3) filled to the fraction |
| `card_bar.c` | `bar` | a bar filled to the fraction (1x1, 1x2, 2x1) |
| `card_graph.c` | `graph` | up to 60 points on one or two lines (blue, violet), a dashed amber threshold, `↓`/`↑` values for two lines (2x2, 2x3, 4x2, 4x3) |
| `card_status.c` | `status` | a dot in the state's colour and the title (1x1, 2x1) |
| `card_pool.c` | `pool` | pool name and health, usage bar, used / size (1x2, 2x2, 1x1) |
| `card_alerts.c` | `nas-alerts` | alert count and level; its default tap opens every alert |
| `card_dots.c` | `dots` | Tailscale and service dots, `+N` for what doesn't fit |
| `card_board.c` | `board` | the Planka boards, flipped vertically, tap an open card to mark it done |
| `card_radar.c` | `radar` | the weather radar loop, conditions, alert badge and the takeover's red frame |
| `card_util.c` | | shared frame, dimming, tap handling, and the dimmed `no gpu 'X'` / `no pool 'X'` note for a missing source |

**General cards and the `values()` hook.** The five general types draw numbers the aggregator has already
formatted; the panel does no formatting. `net.c` polls `GET /api/v1/cards` every 5 s with `If-None-Match` (404
means an aggregator too old for it: 60 s between polls; 5xx keeps what is shown). `cards.c` (host-tested by
`test/cards_test.c`) parses it into `cards_t`. `ui.c` then calls each card's `card_type_t.values(card, entry,
stale)` (in `card.h`): after a new document, when the stale flag flips (no good poll for 30 s), and right after a
rebuild. `values()` only sets text, values and styles; an entry's state (`warn`, `bad`, `stale`, `none`) picks the
colours, the grey-out and the "—" with the aggregator's reason. The GPU card uses the same hook only for its
graph's points. The document is saved to the SD card as `/sdcard/cards.jsn` (at most once a minute, via
`cards.tmp`, like `panel.jsn`) and shown at boot. When a poll fails the pending save is dropped and the held ETag
cleared, so the next poll is a full 200 that re-arms it; an unreadable 200 drops the save too, and the next
changed document re-arms it.

**Memory for the cards document.** `cards_t` is about 31.7 KB (144 entries). There are three copies: the net
task's parse, the shared copy and the UI's, about 95 KB of PSRAM, plus a 20 KB body buffer (`cx_body`), also in
PSRAM. `layout_t` grew about 6 KB per copy for the value, unit, thresholds and GPU slots.

`layout.c` parses the layout document (`GET /api/v1/layout`) into a fixed-size `layout_t`; it is host-tested
(`test/layout_test.c`) and shared with the simulator.

Components (`main/idf_component.yml`, pinned in `dependencies.lock`): `lvgl/lvgl` 9.6,
`espressif/esp_lvgl_port` 2.9, `espressif/esp_lcd_touch_gt911` 1.2.

## Configure

```sh
cp main/secrets.example.h main/secrets.h   # then fill it in
```

`PANEL_TOKEN` is the one in `aggregator/.env`. `WIFI_SSID` must be a 2.4 GHz network that can reach
the aggregator (NAT in between is fine; the panel only makes outbound requests).

A `settings.txt` in the SD card's root can override any of the six without rebuilding: `WIFI_SSID`,
`WIFI_PASSWORD`, `AGGREGATOR_URL`, `PANEL_TOKEN`, `DESKPANEL_TZ`, `NTP_SERVER`, one `KEY=value` per line. It is
read once at boot, before Wi-Fi. A key, file or card that isn't there means the built-in value. The panel never
writes it, and logs only which keys came from it, never a value. How-to and rules:
[docs/getting-started.md, "settings.txt"](../docs/getting-started.md#settingstxt).

## Build

Needs only Docker, and **must run where the files live** (on the machine that holds the checkout, not over a network share).
The IDF component manager can't unpack onto an SMB share: Windows refuses to delete just-emptied
folders there, so every clean build fails.

```sh
sh build.sh              # idf.py build in espressif/idf:v5.5.3 -> build/
sh build.sh fullclean    # any idf.py subcommand works
sh test/run.sh           # host test of the JSON parser; add a saved /api/v1/panel file to parse it too
sh sim/run.sh            # PC simulator: re-render docs/screenshots (see below)
```

## PC simulator

`sim/` runs the panel's real UI code on a PC: `main/ui.c`, `card_*.c`, `takeover.c`, `notify.c`, `panel.c`,
`layout.c`, `astro.c` and `astro_view.c`, compiled against the same LVGL for Linux.
- `sim/sim.c` stands in for `net.c`: it reads one panel document, never goes stale, and makes Done and
  mark-read succeed at once. It serves layouts from `deskpanel demo-layout` (`default.json` from the start).
- It runs headless on a simulated clock.
- It taps on-screen labels the way a finger would.
- It saves a frame after each step.
- `sim/png.py` (standard-library Python) turns the frames into `docs/screenshots/*.png` plus an
  animated `tour.png`.

```sh
sh build.sh              # once, so LVGL is downloaded into managed_components/
sh sim/run.sh            # demo data from the aggregator -> build/sim/deskpanel-sim -> docs/screenshots/
```

The Weather shots come from `deskpanel demo-weather` (the aggregator's demo frames and alerts, inflated by
`run.sh`): `weather-radar.png`, `weather-conditions.png`, `weather-warning.png` (the takeover: red frame
and sticky notice), `weather-alert-sheet.png` (the badge's sheet) and, from a run with no weather
directory, `weather-unset.png` ("Weather isn't set up…"). The second run isn't part of the animated tour.

The clock shots come from `deskpanel demo-astro` (Greenwich; `run.sh` runs it at `DEMO_NOW` + 45 min and
inflates its `sky-*.z` to `sky-*.raw`). The no-weather run opens the clock and shoots `astro-night.png` (8:45 PM,
the mockup's time), `astro-nolocation.png` (the aggregator answers 404) and `astro-day.png` (12:45 PM the next
day: the daylight tint, the sun, "TONIGHT AT 9 PM"). The sim runs in CST, so for these its clock is set to read
Greenwich's time. The checks: the hands' and rings' geometry (in physical pixels: the rim 379 and the day/night
ring 445 px across on 12 diameters, so round on the glass), the column fitting with the longest text the
contract allows, the sky greying when stale, no trails after the hands move, no hands before the time is set.

The plain run ends with the idle tour on the day sky: `tour-clock.png`, `tour-homelab.png`, `tour-slide.png`
(mid-slide to Planka), `tour-planka.png`, `tour-weather.png` and `tour-takeover.png` (a tornado warning arriving
on the tour's clock). They are the last frames of the animated tour. The same run checks the tour's rules on the
sim's clock: 5 min idle, 60 s on the clock and 20 s a tab, the first touch swallowed (on the clock, a card,
mid-slide, mid-fade), the clock button, no tour under a sheet, pop-up or notice, a pop-up or a layout change
stopping it, and the takeover ending it.

A third run (`--layouts`, frames in `build/sim/layout/`, not in the tour) starts with no layout:
`layout-none.png` ("Waiting for the aggregator..."), `layout-404.png` (an aggregator too old for layouts),
then the demo layouts (from `deskpanel demo-layout`, loaded one after another) on the Homelab tab: `homelab-grid.png` (the default), `homelab-gpu1.png`,
`homelab-gpu4.png`, `homelab-gpu0.png`, `layout-unknown-src.png` (moved and resized cards, and a pool card
for a pool that doesn't exist), and `layout-rebuild.png` (a layout change while a sheet was open closes it).

The same `--layouts` run walks `sizes.json` (every card type at every designed size, with `sizes.png`,
`sizes-2.png` and `sizes-wrap.png`) and the long-name variants (`sizes-longnames*.png`,
`homelab-longnames.png`: names as long as a real setup's, and `sizes-longnames-missing.png` for a card whose
source is absent). Each step checks that no card's contents spill out of it.

Three more runs check the board, radar and takeover across layouts, and the first boot:
- `--rebuilt` (frames in `build/sim/rebuilt/`, no PNGs): the same tour after the radar card was dropped and
  brought back. `run.sh` fails unless its frames are identical to the plain tour's.
- `--no-radar` (`build/sim/noradar/`): `noradar.json` and a Tornado Warning. The notice still shows, its
  words open the alert sheet and OK clears it, with no tab change (`takeover-no-radar.png`). Then the layout
  gains a radar card while an escalated notice is up: the notice stays, the panel jumps to the radar's tab
  at once, and the new card shows its red frame (`takeover-jump.png`). After OK, a tab switch sticks. After `takeover-jump`, with the notice still up on "Weather", the radar card moves to a tab called
  "Radar" and the panel must follow it there. Any failed check stops `run.sh`.
- `--no-wifi` (`build/sim/nowifi/`): the generic firmware (below) before its `settings.txt`: Wi-Fi off, no data,
  no layout. The startup screen must say "Wi-Fi isn't set up yet…" and how to fix it (`first-boot.png`).
- `--startup` (`build/sim/startup/`): the startup screen (`main/startup.c`)
  from power-on to the first tab: `startup-starting.png`, `-wifi`, `-aggregator`, `-404`, `-layout` and the two
  fades. It must stay while there is no layout and go once there is one. The logo looks about 6 % narrow in
  these shots: it is drawn for the panel's pixels. It fades in over 0.6 s, shows for at least 2 s
  (`STARTUP_HOLD_MS`), then fades into the tabs. The other runs skip it (`startup_off`), so their frames are as
  before. The logo image is `main/startup_logo.rgb565`, made on a PC with Pillow from a 312x244 PNG of the logo block (already narrowed
  for the panel, on the background colour):
  `python -c "from PIL import Image; import struct; open('firmware/main/startup_logo.rgb565','wb').write(b''.join(struct.pack('<H',(r>>3)<<11|(g>>2)<<5|b>>3) for r,g,b in Image.open('logo-312x244.png').convert('RGB').getdata()))"`

Changed a page? Re-run `sh sim/run.sh` and look at the PNGs before flashing. It catches layout and tap
problems in seconds: its first run found a tile header that swallowed taps. If a scripted tap can't find
its label, it stops with `nothing labelled "…" on screen`. The walk-through is at the end of `sim/sim.c`.

## Pages

- **Status bar**: clock, stale pill, notification bell (unread count; tap for the history),
  Wi-Fi. `main/notify.c` handles notifications: the newest unread one pops up with OK, then falls
  back to the bell after 30 s; reads are reported to the aggregator.
- **Tabs come from the aggregator's layout** (`GET /api/v1/layout`): each tab is a 4 × 6 grid of cards
  (`main/card_*.c`, placed by `ui.c`), rebuilt whenever the layout changes (an open sheet closes).
  A card whose source isn't in the data says so, dimmed: `no gpu 'X'`, `no pool 'X'`.

**How the tabs are built.** `net.c` polls `GET /api/v1/layout` (with `If-None-Match`) next to the panel
document. `ui.c`'s 500 ms timer takes the newest layout and, when it differs from the one on screen, tears
the tabs down and builds them again from the layout: one tab per entry, one card per card, placed on the 4 x 6
grid. An open sheet closes. A card's `tap` is
attached to its whole box; `none` makes the card inert. The status per poll is `net_layout_status()`:
- **200 / 304**: fine.
- **404**: an aggregator too old for layouts; if no layout is showing, the screen says "Update the aggregator to use layouts".
- **5xx** (the aggregator is up but has no layout ready yet, up to 25 s after its restart): **keeps what's
  shown**. No back-off, no SD write; with nothing shown yet the screen says "Waiting for the aggregator...".
- **-1** (unreachable): the back-off doubles, as for the panel document.

**The takeover doesn't depend on the layout.** See Weather below: with or without a radar card, and
whatever the radar's tab is called, the notice shows; a layout change never clears it.
- **Homelab** (the default layout): a gpu card per GPU (temperature arc, VRAM bar, idle / loaded / busy /
  off), a pool card per pool (health, usage bar) plus a NAS alerts card (tap for every alert in full;
  violet frame = info only), then a dots card: Tailscale peers and service checks on one line, `+N` for
  what doesn't fit (red if a hidden service is down). Cards turn amber or red on high temperature, a
  filling pool, an unhealthy pool or alert severity.
- **Planka**: one tile per board the aggregator's `deskpanel` user is a member of. **Swipe up/down**
  to flip boards (left/right still switches pages); it also flips by itself every 10 s unless touched
  in the last 30 s. Each board shows its 3 most recently updated cards. Tap an open card → confirm
  sheet → **Done**: the card shows done straight away, the network task POSTs it, and a failure
  puts it back with a message.
- **Weather** (`main/card_radar.c`, `main/takeover.c`, `main/weather.c` parser, `main/wx_icon.c`): the aggregator's
  `GET /api/v1/weather`, polled every 15 s with `If-None-Match`. Five 800×368 radar frames loop
  full-bleed (550 ms each, 1.4 s on the newest, 3 loops), then fade to the conditions for 7 s, and back.
  - Chip with the frame's age and time (amber when the newest frame is over 15 min old), frame dots,
    warning outlines (red tornado, yellow severe; home's is thicker and pulses), and the alert badge
    (amber watch, red pulsing warning, yellow-amber advisory, `+N` for more). Tap the badge for a
    sheet with a chip per alert in one row that scrolls sideways (tap one for its full text, fetched on
    demand from `/alert/{n}`; an off-screen one scrolls into view). The text is laid out as the
    mockup's: headline (20 px), issued/expires (14 px, dim), each description paragraph with its
    leading `KEYWORD...` in yellow (`wx_markup()`: LVGL recolor text), and the instruction in a block
    with a red left edge. Type sizes follow the mockup: 14 px time chip, home label and chips,
    22 / 17 px condition and high/low (shared with the astronomical clock), a 12 px ⚠ on the advisory badge.
  - A takeover alert (`take`) jumps to this tab, shows a red frame and a sticky notice that only its
    own OK clears. Wording follows the escalation level (`lvl` 0-3: warning, tornado observed,
    considerable damage threat, emergency). The notice sits above the map's "© OpenStreetMap
    contributors" credit and stays in front of toasts and notification pop-ups (`takeover_raise()`);
    a toast shown while it's up goes just above it, so it stays readable. A sheet opened after it
    covers it until the sheet closes.
  - The takeover doesn't need the radar card. With no radar card in the layout, the notice still shows
    and OK still clears it. There's no jump, and tapping the notice's words opens the alert sheet.
    Radar polling and frames keep running either way. A layout change never clears the notice. If a
    radar card appears while the notice is up (for example, the first layout after boot), the panel jumps
    to it straight away.
  - A 404 from the aggregator draws "Weather isn't set up on the aggregator".
  - Bold text uses two subset fonts (`Montserrat-ExtraBold-sub.ttf`, `-SemiBold-sub.ttf`: ASCII + ° · −).

  **Memory.** A frame is 800 × 368 × 2 = **588,800 B**, so the radar holds **6 PSRAM frame buffers**
  (5 on screen + 1 downloading), about 3.5 MB of the 8 MB. They are allocated once at boot while PSRAM
  is in one piece, and frames are inflated straight into a free buffer as they stream in (ROM `tinfl`;
  no compressed copy is kept). The log shows the figures: `frames: 6 buffers, PSRAM free … KB` at boot
  and `frames: first full set, PSRAM free … KB` after the first five arrive. The target is **at least
  1 MB free**; if the 6-buffer set leaves less, it drops to **5 buffers** (`frames: 5 buffers…`
  warning) and the loop briefly shows 4 frames while one is replaced.

Built-in LVGL fonts are ASCII only, so Planka text (em dashes, curly quotes, accents) is drawn from
the embedded Montserrat TTF via Tiny TTF; `LV_SYMBOL_*` icons fall back to the built-in fonts.

## Astronomical clock and idle tour

**The view** (`main/astro_view.c`) is one full-screen object, the screen's last child: over the status bar and
the tabs, under the top layer's notices, toasts and sheets. Everything in it is made once at boot; after that only
guarded setters run, so an unchanged value costs no redraw (redraw load makes the RGB panel drift).
- The aggregator draws the round sky: a 378 × 378 RGB565 picture. The panel fits it to its own pixels once when
  it arrives (see "Round on the glass" below) and places it inside the disc.
- The panel draws the rest, crisp and live: the day/night ring from the document's `day` minutes, the 24 hour
  ticks, `00 06 12 18`, N/E/S/W over a dark halo, the gold rim, the column, and the hands. The hands are lines
  with a drop shadow, each drawn twice (a shadow-casting copy under all the pieces) and moved once a minute. The
  hour hand's ring is filled by a 22 × 24 ARGB image to the moon's phase, redrawn when the whole percent lit
  changes.
- Opening fades in over 300 ms, then hides what it covers: LVGL doesn't skip an object another covers, so the
  radar loop, the board's paging and the RSSI would keep repainting under the clock. Closing shows them again
  first, then fades out. A tap on the view closes it, and the view takes the whole touch.
- The time, date and hands come from the panel's own clock (`--:--` and no hands until it's set). No good poll
  for 10 min, or the aggregator's `stale`, greys the sky to 60 %. A 404 hides the sky and the rows and shows
  "Set WEATHER_LAT/LON for the sky".

**Round on the glass.** The panel's pixels are about 5.9 % wider than tall (a 16:9 glass carrying 800 × 480), so
a circle drawn in pixels looks wide (the dial measured 55 mm × 52 mm). `PANEL_ASPECT` in `main/astro.h`, pixel width
÷ pixel height (1.0587), narrows everything round by that much: the clock, and on the cards the gauges, the radar's
home ring and the weather icon. The shared pieces live in `main/ellipse.c`: `ellipse_w(h)` (the width that's h on
the glass) and the fitted ellipse arcs below. On the clock:
- **The sky**: `astro_sky_fit()` resamples each new picture in place into the first 357 columns of each row
  (`SKY_FIT_W`), its middle on the dial's centre, and paints everything outside the dial (`DIAL_R`) the screen's
  colour. The rows keep their 378-pixel stride,
  so the download, the two buffers and `sky.raw` keep their size. It runs once per picture, in the net task, before
  the UI or the SD copy sees it. A `sky.raw` saved by older firmware shows unfitted (cut at the right) until the
  first download, at most about 2 min.
- **Positions**: the ticks, hour numbers, N/E/S/W and hands all come from `polar()`, which narrows x.
- **Shapes**: the discs, the hour hand's ring and the centre cap are `ellipse_w()`-narrow stadiums (LVGL has no
  ellipse; the plain sky's is 2 px smaller so it stays under the rim). The day/night ring and the gold rim are true
  ellipses, each drawn as 8 arcs of circles fitted to it at boot, within half a pixel (`ellipse_fit()`; the night
  arcs only outside the daylight arc, not under it). They're arcs, not line
  segments: LVGL culls a line by its ends ± width / 2 while its anti-aliasing reaches further, so a redraw's edge
  (the hands each minute, the sky every 2 min) leaves marks on a line.
- **To retune**: measure the dial's outer ring on the glass and multiply `PANEL_ASPECT` by its width_mm ÷
  height_mm, then rebuild (the cards follow the same knob). When it's tuned the ring measures 1.00 (equal width
  and height), so a reading of, say, 1.01 means PANEL_ASPECT × 1.01. In the simulator, whose pixels are square,
  the clock and the gauges look about 6 % tall. That's expected.

On the cards:
- **Gauges** (the gauge card's and the GPU card's arcs) stay `lv_arc`s, so value, range, angles, colours and round
  caps are LVGL's, but `ellipse_gauge()` turns the arc's own drawing off and draws its track and value as fitted
  ellipses, the outer edge `ellipse_w(size)` wide: the 100 px gauges are 94 px wide, the 80s 76, the 76s 72, the
  small 54 51 (its ring is 3 px, so "100%" still clears it). The fit runs once, when the card is made. A value
  change goes through `ellipse_gauge_set()`, which redraws the whole gauge (at most 100 × 100 px): the ellipse
  pokes out of the patch LVGL would redraw for a round arc, and that would leave marks. A greyed gauge (stale, or
  an offline GPU) draws each part into a small layer first, so it fades evenly with no lighter seams.
- **The radar's home ring** is `ellipse_w(28)` × 28 (the 14 px dot in it is too small to matter).
- **The weather icon** squeezes every x about its middle column (`sx()` in `main/wx_icon.c`), so its discs are
  stadiums like the clock's. The icon's box keeps its width, so the overlay doesn't move.

If a gauge looks like it leaves marks when its value changes, the simulator checks that (`redraw_clean()`: a full
redraw after a value change must change nothing); its `gauges_round()` checks each gauge's width.

**Data** (`main/net.c`, parsed by `main/astro.c`). `GET /api/v1/astro` every 60 s with `If-None-Match`, last in
the poll loop on its own due time and back-off (never the shared one), so a slow document or sky delays the
weather, panel and cards polls by at most its own time; after a 404 (no location) every 10 min. When the
document names a sky time the panel doesn't hold, `GET /api/v1/astro/sky/{t}` is inflated (ROM `tinfl`) into a
buffer the UI isn't drawing, and swapped in only if the answer is 200, the inflate finishes and exactly 285,768
bytes arrived within 15 s. Anything less leaves the old picture.

**SD copy.** `/sdcard/astro.jsn` (the document, at most once a minute, via `astro.tmp`) and `/sdcard/sky.raw`
(the raw picture, at most every 6 h, via `sky.tmp`), both shown at boot before Wi-Fi. A `sky.raw` of any other
size is ignored.

**Memory.** Two PSRAM sky buffers of 378 × 378 × 2 = **285,768 B** (about 558 KB), allocated first at boot,
before the radar frames, while PSRAM is in one piece. The boot log says `sky: 2 buffers of 279 KB, PSRAM free …
KB`. If the second doesn't fit (`(single-buffered)` in that line), each download goes into a temporary buffer and
is copied over when complete; that copy can tear one redraw, and the next one is whole. The radar's own check
(at least 1 MB free, else 5 frame buffers) runs after, so it sees the sky buffers.

**The idle tour** is a small state machine in `ui.c` (`tour_tick()`, on the 500 ms timer):
- It starts after `lay.tour_idle_ms` (5 min by default) of LVGL inactivity, when the layout has tabs, `tour_on` and
  at least one step to show, and the clock isn't open by hand. While a sheet, a notification pop-up or the takeover notice is up it calls
  `lv_display_trigger_activity()`, so the 5 min start only when that goes.
- Steps: the clock `tour_clock_ms` (60 s), then each tab `tour_tab_ms` (20 s) in layout order, then the clock.
  The timings, `tour_clock` (false leaves the clock step out), `tour_on` and `tour_skip` (bit k = tab k skipped)
  come from the layout document's optional `tour` block, parsed in `layout.c` (every key optional; missing or
  wrong-typed gives the defaults, idle is clamped to 60 s..2 h, the stays to 5..600 s).
  The clock gives way to the first tab under its own fade (no slide across the tabs); tab to tab is the tabview's
  slide; the last tab to the clock is the clock's fade-in.
- `catcher`, an invisible full-screen object (the top layer's first child, so notices, toasts and sheets stay
  above it and their buttons work), is shown only while the tour runs. It takes the first touch and calls
  `lv_indev_wait_release()`, so nothing under it is pressed for the rest of that touch. On the clock it goes back
  to `home`, the tab from before the tour.
- `ui_tour_stop()` ends it: a notification pop-up, the takeover's jump (`takeover.c`, which also closes a clock
  opened by hand) and a layout rebuild all call it.

**The clock button.** The status bar's time is clickable, with a 16 px wider tap area, and opens the view.

## Offline behaviour

The screen never goes blank.
- **Before the first layout** (and with no SD copy of one) it says "Waiting for the aggregator..." and,
  before the first panel document, the status bar reads **NO DATA**. An aggregator too old to serve
  layouts gets "Update the aggregator to use layouts".
- **With no good poll (200 or 304) for 30 s** it reads **STALE 45s** / **STALE 3m** / **STALE 3h**
  (just **STALE** past two days) and greys the pages, still showing the last data.
- **A section the aggregator itself marks stale** is dimmed on its own page.
- **The astronomical clock** keeps its time and hands from the panel's clock, and the last sky and column; the
  sky greys after 10 min without a good poll. With no data at all (and no SD copy) it shows a plain dark sky and
  "—".

**SD card copy.** With a FAT-formatted microSD card in the slot:
- The latest document is written to `/sdcard/panel.jsn` at most once a minute. Every poll changes
  it (GPU numbers), and writing that often would wear the card.
- The layout document is kept the same way as `/sdcard/layout.jsn`, written only when it changes. It is shown
  at boot before Wi-Fi, so the tabs exist offline. With no card, the first layout comes from the aggregator.
- Both files are written to a temporary name first (`panel.tmp`, `layout.tmp`) and then renamed over the real
  one, so a power cut never leaves half a file. If the cut falls between the remove and the rename, boot
  reads the `.tmp` copy instead.
- At boot the copy is shown straight away, before Wi-Fi, aged from its file time. Its notes count as
  read, so they appear in the history but don't pop up again.
- `/sdcard/boot.log` gets one line per boot: when, the boot number, why (power-on, crash (task watchdog), ...)
  and, after a reset, how long the previous run lasted and when it was last seen. It stays under 16 KB (the
  oldest lines are dropped) and is written via `boot.tmp` like the others. The previous run's numbers live in
  RTC memory, updated once a minute, so a power cut leaves them unknown: that boot reads "power-on".
- `/sdcard/settings.txt`, if there, is read once at boot (≤ 4 KB) and never written.
- No card, or a card that isn't FAT, just logs a line and carries on. The card is never formatted.

## Self-restart

If the screen or the network side hangs, the panel restarts by itself instead of staying frozen until it is
replugged. The task watchdog (panic on timeout, 120 s) watches:
- **the UI** (the LVGL task), fed by `ui.c`'s 500 ms timer, which runs in every state: pages, clock, tour, sheets,
  notices, "Waiting for the aggregator...", Wi-Fi off.
- **the network task** (`net.c`), fed once per pass, per tap it serves, before each request, as a radar frame or
  sky picture arrives, and before the sky picture's SD write. Its longest legitimate stretch between feeds is one
  slow request: about 20 s with an IP address in `AGGREGATOR_URL`, 42–54 s with a host name whose DNS is down.
  With Wi-Fi off it never starts, so there is nothing to watch.
- **the two idle tasks.** Before, a starved one only logged a warning after 5 s; now one starved for 120 s (a task
  hogging a core) restarts the panel.

A watchdog restart shows up like any crash: a backtrace on the serial log naming the stuck task, `crash (task
watchdog)` on the boot line and in `/sdcard/boot.log`, and an amber bell note "Panel restarted after a crash (task
watchdog), up … before". A rare one is the panel recovering on its own; repeats mean something keeps hanging, so keep
the serial log. The 120 s is set in `main.c` (`timeout_ms`): `sdkconfig` can't go past 60 s, and a bigger
`CONFIG_ESP_TASK_WDT_TIMEOUT_S` silently falls back to 5 s.

**Testing it.** A test build hangs the UI task on purpose 30 s after boot (`wdt test: hanging the UI task` on the
serial log); about 2 min later the panel restarts and the bell shows the note. Never leave it flashed:

```sh
sh build.sh -DDESKPANEL_WDT_TEST=1 build   # the test build; flash it and watch the serial log
sh build.sh -DDESKPANEL_WDT_TEST=0 build   # back to normal (a plain build keeps the last value)
strings build/deskpanel.bin | grep -c "wdt test"   # 0 = the hook is out
```

## Release build: the flasher page's firmware

The firmware on [the flasher page](https://zebadrabbit.github.io/deskpanel/) is generic: no Wi-Fi, address or
token built in. Each panel gets them from `settings.txt` on its SD card (the page makes that file).

```sh
sh release.sh   # -> ../web/firmware/deskpanel-<version>.bin (one image for offset 0), ../web/manifest.json, ../web/img/
```

- It builds with `-DDESKPANEL_GENERIC=1`, so `net.c` includes `secrets.example.h` and never reads `secrets.h`.
  CI builds it from a fresh checkout, where `secrets.h` doesn't exist.
- Its own build dir and sdkconfig (`build-release/`, the sdkconfig made from `sdkconfig.defaults`): your `build/`
  and `sdkconfig` are never touched.
- `idf.py merge-bin` joins bootloader, partition table and app into one image, written at 0x0.
- The version is `git describe --tags --always --dirty` (`dev-<date>` outside a git checkout).
- The outputs are gitignored; `web/img/` gets the page's two simulator screenshots. `.github/workflows/pages.yml`
  runs `release.sh` on every push to main and publishes `web/` to GitHub Pages. The page loads nothing from
  anywhere else: ESP Web Tools is vendored in `web/vendor/` (see its README).
- With Wi-Fi unset, the startup screen says so and points at the page (sim: `first-boot.png`). A panel with Wi-Fi
  built in, or set in `settings.txt`, never shows it.

**Try the page locally.** Web Serial only works on `https://` or `http://localhost`, so serve `web/` on the
computer the panel is plugged into, or tunnel to it:

```sh
sh release.sh && cd ../web && python3 -m http.server 8000       # then open http://localhost:8000 in Chrome/Edge
ssh -L 8000:localhost:8000 <server> 'cd <repo>/web && python3 -m http.server 8000 --bind 127.0.0.1'   # from the PC
```

Flashing it replaces whatever is on the panel (it erases first). To go back to your own build, run `flash.ps1`.

## Flash and monitor

From the PC the panel is plugged into (USB-C, native USB Serial/JTAG; it shows up as a COM port,
e.g. `COM11`, which is `flash.ps1`'s default):

```powershell
powershell -ExecutionPolicy Bypass -File flash.ps1            # flash build/, then open the serial log
powershell -ExecutionPolicy Bypass -File flash.ps1 -Port COM7 -NoMonitor
```

It uses PlatformIO's bundled esptool if installed, otherwise `python -m esptool`
(`pip install esptool`).

Each boot prints one line to the serial log, e.g.
`2026-11-23 14:05  boot 12: crash (task watchdog); previous run up 3 d 4 h, last seen 2026-11-23 14:04`.
The same report, `X-Panel-Boot: 12 task_wdt 273600` (boot number, reason, the previous run's uptime in
seconds, 0 if unknown), is sent on every panel poll.

## Board notes

Pins, RGB timings and the CH422G and GT911 sequences come from Waveshare's official 4.3B demo
(`ESP32-S3-Touch-LCD-4.3B-BOX-Demo.zip`, `ESP-IDF/08_lvgl_Porting`) and the board schematic. The demo
is LVGL 8 with the legacy I2C driver; here it's LVGL 9 via `esp_lvgl_port` and `i2c_master`.

- **RGB panel**: 800×480 RGB565 at 16 MHz PCLK, HSYNC/VSYNC 4/8/8. Two framebuffers in PSRAM and a
  10-line bounce buffer in internal RAM; LVGL draws directly into the back buffer and swaps on vsync.
- **Drift under Wi-Fi/flash**: code and rodata run from PSRAM (`SPIRAM_FETCH_INSTRUCTIONS`, `RODATA`),
  IRAM-safe RGB ISR, Wi-Fi/LWIP buffers kept out of PSRAM, Wi-Fi config held in RAM so it never writes
  flash (see `sdkconfig.defaults`).
- **I2C0** (SDA 8, SCL 9, 400 kHz) is shared by the CH422G, the GT911 (0x5D) and the RTC (0x51).
- **CH422G**: EXIO1 touch reset, EXIO2 backlight + panel DISP, EXIO3 LCD reset, EXIO4 SD CS.
- **Backlight is on/off only.** EXIO2 drives the boost converter's enable; no PWM path exists, so
  dimming needs a hardware change. A pull-up keeps it on before the CH422G is initialised.
- **Clock**: the RTC holds UTC. It sets the clock at boot and NTP corrects it (and the RTC) once Wi-Fi
  is up. The timezone is `DESKPANEL_TZ` in `secrets.h` or `settings.txt`.
- **SD card**: SPI2 with MOSI 11, MISO 13, CLK 12 (Waveshare's `03_SD_Test` demo). CS is CH422G EXIO4,
  held low while mounted (it's the bus's only device).
  - Because CS can't be toggled, ESP-IDF's SDIO probes during card init (CMD52, CMD5) are replaced at
    link time (`--wrap`, see `main/CMakeLists.txt` and `board.c`).
  - Without that, the timed-out CMD5 leaves its late reply on the line and CMD59 "fails".
  - Long file names are off, hence the 8.3 names, `settings.txt` included.
