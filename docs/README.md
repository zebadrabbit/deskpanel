# deskpanel documentation

New here? Start with [Getting started](getting-started.md). The [front page](../README.md) says what deskpanel is.

| Page | What's in it |
|------|--------------|
| [Getting started](getting-started.md) | Install from the flasher page and the prebuilt image, or build your own. First boot and the startup screen, `settings.txt`, the `.env` essentials. |
| [What's on the screen](screen.md) | The status bar, the Homelab, Planka and Weather pages, notifications, the idle tour and the astronomical clock, setting up the weather. |
| [Your own tabs: layout.json](layout.md) | The layout file, the grid, card types and sizes, taps, limits, what happens when you save, the `tour` block. |
| [Cards](cards.md) | General cards from Prometheus, an app's JSON status page or Home Assistant. GPU card slots. What a card's small text means. |
| [The screen editor](editor.md) | Lay out tabs in the browser: `EDITOR_TOKEN`, saving, the backup file, the made-up-data demo. |
| [How do I…](how-to.md) | Recipes: Planka boards, notifications from other tools, Claude Code hooks, service dots, GPU machines, updates, tokens. |
| [Troubleshooting](troubleshooting.md) | "When something looks wrong", row by row, and where everything lives. |

## Words used in these pages

In the commands on these pages, **the server** is the machine running the aggregator (`<server-ip>` is its LAN
address), **the PC** is the computer the panel is plugged into over USB, and `<repo>` is your checkout
of this repository.

## Internals

These two are developer-level references:

- [aggregator/README.md](../aggregator/README.md): the API and protocol, every `.env` variable, build and test, deploying
  the aggregator and the GPU agents.
- [firmware/README.md](../firmware/README.md): building and flashing, the PC simulator, the pages, offline behaviour,
  the release build, board notes.

## What's in the repository

| Dir           | What                                                                                        |
|---------------|---------------------------------------------------------------------------------------------|
| `aggregator/` | Go service, plus an `agent` mode that wraps `nvidia-smi` on each GPU host. Deploys with Docker Compose. `deploy/` has the GPU agent installers and the Claude Code hooks. |
| `firmware/`   | ESP-IDF 5.5 + LVGL 9, built in the official ESP-IDF Docker image. Board support from Waveshare's official 4.3B demo. `sim/` renders the UI on a PC. |
| `docs/`       | This guide, and its screenshots, made by `firmware/sim/run.sh`. |
| `web/`        | The flasher page (ESP Web Tools, vendored in `web/vendor/`, and the `settings.txt` maker). `firmware/release.sh` adds the generic firmware, and `web/build-editor.sh` adds the screen editor's made-up-data demo (`web/editor/`, copied from `aggregator/editor/`); `.github/workflows/` publish it to GitHub Pages and the aggregator image to ghcr.io. |
