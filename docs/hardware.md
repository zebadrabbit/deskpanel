# The hardware

deskpanel's firmware is written for one board. This page says what to buy, what else you need, and what it
would take to run it on something else.

## What to buy

**The Waveshare ESP32-S3-Touch-LCD-4.3B.** It is a 4.3″, 800×480 capacitive touchscreen with an ESP32-S3
(16 MB flash, 8 MB PSRAM), a microSD slot and a clock chip on one board. No soldering and no wiring: it is one
piece, powered and flashed over USB-C.

- **Where:** Waveshare sells it directly, and through its storefronts on Amazon and AliExpress. The author's
  came from [this Amazon listing](https://www.amazon.com/dp/B0DD6VLH1Y) for about $55 in 2026. Prices and
  listings change, so if that one is gone, search for the exact name, `ESP32-S3-Touch-LCD-4.3B`. It is a
  common board and easy to find.
- **Check the B.** Waveshare also makes an `ESP32-S3-Touch-LCD-4.3` without the B. It is a different board
  (see [Other boards](#other-boards)), and listings for the two look alike.
- **Manufacturer's page**, with the schematic and the demo code:
  [docs.waveshare.com/ESP32-S3-Touch-LCD-4.3B](https://docs.waveshare.com/ESP32-S3-Touch-LCD-4.3B).

You also need:

- **A microSD card**, formatted FAT32. Any size works; the panel stores a few small files on it.
  - With the firmware from the [flasher page](https://zebadrabbit.github.io/deskpanel/) the card is required: it
    carries `settings.txt` (Wi-Fi, server address, token).
  - If you build the firmware with your own values, the card is optional. Without it the panel starts empty
    after a power cut instead of showing its last data.
- **A USB-C cable that carries data**, for flashing. Afterwards any 5 V USB supply powers it. A weak supply or
  cable shows up as restarts ("brownout" in the [restart notes](how-to.md#why-did-the-panel-restart)).
- **A 2.4 GHz Wi-Fi network.** The ESP32-S3 has no 5 GHz radio.
- **Something to stand it on.** The bare board has no stand; a small phone stand or a printed one does the job.

The other half runs on a machine you already have: any Linux box with Docker, amd64 or arm64
([Getting started](getting-started.md)).

## What the firmware relies on

This is why the board is named so exactly. `firmware/main/board.c` and the UI assume:

| Part | On the 4.3B | Used for |
|------|-------------|----------|
| Screen | 800×480 RGB panel, 16-bit colour | Every page is laid out for 800×480. Its pixels are about 6 % wider than tall, and round things are drawn narrower to match (`PANEL_ASPECT`). |
| Touch | GT911, capacitive | Taps and swipes |
| Memory | 8 MB PSRAM, 16 MB flash | Two screen buffers, the radar frames and the sky picture live in PSRAM |
| IO expander | CH422G | Backlight on/off, touch and screen reset, the SD card's chip select |
| Clock chip | PCF85063A | The time at boot, before Wi-Fi is up |
| microSD | SPI | `settings.txt`, the last data, the boot log |

The backlight is on or off only. This board has no dimming path, so the panel cannot dim at night.

**The connectors on the back are not used yet.** The 4.3B also brings out RS485, CAN and spare IO, which is part
of why the project stays on this board: there are plans for them, and they are not finished.

## Other boards

**Nothing else is supported or tested today, and the author knows of no drop-in replacement.** The flasher
page's firmware is built for the 4.3B and will not drive a different board. At about $55 the 4.3B is cheap
enough that buying it is less work than porting.

Running deskpanel on another board is a port, and its size depends on how far the board is from this one:

- **Same screen size, different wiring** (another 800×480 ESP32-S3 RGB board with 8 MB PSRAM): rewrite
  `firmware/main/board.c` for its pins, panel timings, touch chip and backlight. Check the pixel shape and set
  `PANEL_ASPECT` in `firmware/main/astro.h`. The UI should need little else.
- **No clock chip or no SD slot:** the clock then waits for Wi-Fi and NTP, and without a card there is no
  `settings.txt` and nothing is kept across restarts. Both need small changes where the firmware expects them.
- **A different resolution** (the 5″ and 7″ boards at 1024×600, small round displays): the pages, the card
  grid, the radar frames and the sky picture are all sized for 800×480, on the panel and on the aggregator.
  That is real UI work, not a pin change.
- **Less than 8 MB of PSRAM:** the radar loop and the astronomical clock will not fit as they are.

Waveshare's other ESP32-S3 touch boards (the 4.3 without the B, the 5″ and the 7″) are the closest relatives,
but none has been tried. If you port it, [open an issue](https://github.com/zebadrabbit/deskpanel/issues) or a
pull request: board support in its own file is the natural shape for it.

Board details for developers (pins, timings, the SD card workaround) are in
[firmware/README.md](../firmware/README.md#board-notes).
