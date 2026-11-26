#!/bin/sh
# Render the README screenshots with the PC simulator: the panel's real UI code on LVGL for Linux,
# fed the aggregator's demo data. Needs Docker, and one `sh build.sh` first (it downloads LVGL into
# managed_components). Usage: sh sim/run.sh [framesdir]. Writes docs/screenshots/*.png and the animated docs/screenshots/tour.png.
set -e
cd "$(dirname "$0")/.." # firmware/
export DEMO_NOW=1795032000 TZ=CST6CDT # a fixed instant, so every run draws the same frames
out="${1:-build/sim}" # where the PPM frames go (relative to firmware/); build/sim stays the work dir
rm -rf build/sim/wx build/sim/unset build/sim/layouts build/sim/astro
mkdir -p build/sim/wx build/sim/unset
user="$(id -u):$(id -g)"
# The clock's sky (Greenwich) is drawn 45 min after DEMO_NOW, 8:45 PM there (the mockup's time); the day sky 16 h later.
docker run --rm -u "$user" -e HOME=/tmp -e DEMO_NOW -v "$PWD/../aggregator":/src -v "$PWD/build/sim":/out -w /src \
    golang:1.26-alpine sh -c 'go run . demo-json >/out/demo.json && go run . demo-weather /out/wx &&
        go run . demo-layout /out/layouts && go run . demo-cards /out/layouts &&
        DEMO_NOW=$((DEMO_NOW + 2700)) go run . demo-astro /out/astro'
docker run --rm -u "$user" -e HOME=/tmp -e OUT="$out" -e DEMO_NOW -e TZ -v "$PWD/..":/repo -w /repo/firmware espressif/idf:v5.5.3 sh -ec '
  L=managed_components/lvgl__lvgl
  J=$IDF_PATH/components/json/cJSON
  INC="-Isim -Isim/stubs -Imain -I$L -I$L/src -Imanaged_components"
  if [ ! -f build/sim/liblvgl.a ]; then # LVGL itself only once
    mkdir -p build/sim/lvgl
    find $L/src -name "*.c" | xargs -P 8 -I{} sh -c "gcc -O2 -w $INC -c {} -o build/sim/lvgl/\$(echo {} | tr / _).o"
    ar rcs build/sim/liblvgl.a build/sim/lvgl/*.o
  fi
  (cd main/fonts && ld -r -b binary -z noexecstack -o ../../build/sim/ttf.o Montserrat-Medium.ttf \
      Montserrat-ExtraBold-sub.ttf Montserrat-SemiBold-sub.ttf)
  (cd main && ld -r -b binary -z noexecstack -o ../build/sim/logo.o startup_logo.rgb565)
  gcc -O1 -Wall $INC -I$J sim/sim.c main/ui.c main/startup.c build/sim/logo.o main/card_*.c main/takeover.c \
      main/wx_icon.c main/notify.c main/panel.c main/weather.c main/layout.c main/cards.c main/astro.c main/astro_view.c main/ellipse.c $J/cJSON.c build/sim/ttf.o \
      build/sim/liblvgl.a -lm -o build/sim/deskpanel-sim
  # The image has no zlib headers, so inflate the radar frames and skies here: frame-<t>.z -> frame-<t>.raw, sky-<n>.z -> sky-<n>.raw
  python3 -c "import glob, zlib
for p in glob.glob(\"build/sim/wx/frame-*.z\") + glob.glob(\"build/sim/astro/sky-*.z\"): open(p[:-1] + \"raw\", \"wb\").write(zlib.decompress(open(p, \"rb\").read()))"
  rm -rf "$OUT/unset" "$OUT/layout" "$OUT/rebuilt" "$OUT/noradar" "$OUT/nowifi" "$OUT/startup" "$OUT/lights" "$OUT"/*.ppm
  mkdir -p "$OUT/unset" "$OUT/layout" "$OUT/rebuilt" "$OUT/noradar" "$OUT/nowifi" "$OUT/startup" "$OUT/lights"
  S="build/sim/deskpanel-sim" D="build/sim/demo.json" L="build/sim/layouts"
  $S $D $L "$OUT" build/sim/wx
  $S --rebuilt $D $L "$OUT/rebuilt" build/sim/wx # the tour after two layout changes: frames to compare, no PNGs
  python3 sim/compare.py "$OUT" "$OUT/rebuilt" >/dev/null || { python3 sim/compare.py "$OUT" "$OUT/rebuilt"; exit 1; }
  $S $D $L "$OUT/unset" # weather not set up: a shot of its own
  $S --layouts $D $L "$OUT/layout" # no layout yet, then the demo layouts
  $S --no-radar $D $L "$OUT/noradar" build/sim/wx # the takeover without a radar card
  $S --no-wifi $D $L "$OUT/nowifi" # the generic firmware (flasher page), no settings.txt yet
  $S --startup $D $L "$OUT/startup" # the startup screen, power-on to the first tab
  $S --lights $D $L "$OUT/lights" # the light and profile cards against a fake Home Assistant
  python3 sim/png.py "$OUT" ../docs/screenshots
  for d in unset layout noradar nowifi startup lights; do # (not in the tour)
    python3 sim/png.py "$OUT/$d" "$OUT/$d" && rm "$OUT/$d/tour.png" && cp "$OUT/$d"/*.png ../docs/screenshots/
  done'
