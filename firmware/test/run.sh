#!/bin/sh
# Host-side test of the JSON parser, using the IDF image's gcc and cJSON. Optional arg: a JSON file
# to parse and summarise (e.g. a saved /api/v1/panel response).
set -e
cd "$(dirname "$0")/.."
docker run --rm -u "$(id -u):$(id -g)" -v "$PWD":/project -w /project espressif/idf:v5.5.3 sh -ec '
  J=$IDF_PATH/components/json/cJSON
  gcc -std=gnu11 -Wall -Wextra -I main -I $J -o /tmp/panel_test test/panel_test.c main/panel.c $J/cJSON.c
  gcc -std=gnu11 -Wall -Wextra -I main -I $J -o /tmp/weather_test test/weather_test.c main/weather.c main/panel.c $J/cJSON.c
  gcc -std=gnu11 -Wall -Wextra -I main -I $J -o /tmp/layout_test test/layout_test.c main/layout.c $J/cJSON.c
  gcc -std=gnu11 -Wall -Wextra -I main -I $J -o /tmp/cards_test test/cards_test.c main/cards.c $J/cJSON.c
  gcc -std=gnu11 -Wall -Wextra -I main -I $J -o /tmp/astro_test test/astro_test.c main/astro.c $J/cJSON.c
  gcc -std=gnu11 -Wall -Wextra -I main -o /tmp/bootlog_test test/bootlog_test.c main/bootlog.c
  gcc -std=gnu11 -Wall -Wextra -I main -o /tmp/settings_test test/settings_test.c main/settings.c
  /tmp/panel_test "$@"
  /tmp/astro_test
  /tmp/weather_test
  /tmp/layout_test
  /tmp/cards_test
  /tmp/bootlog_test
  /tmp/settings_test' sh "$@"
