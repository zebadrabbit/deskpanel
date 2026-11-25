// The startup screen: the logo with a sine wave scrolling through its chart box, a status
// line and three dots (Wi-Fi, aggregator, layout). It fades in from the dark, stays at least STARTUP_HOLD_MS, then
// fades into the tabs once there is a layout (the SD card's copy counts); with none it stays and says what it is
// waiting for. Over the bar and the tabs, under the top layer: notifications and the tornado notice show over it.
// All on the LVGL task.
#pragma once

#include <stdbool.h>

#include "lvgl.h"

#define STARTUP_HOLD_MS 2000 // fully shown at least this long

extern bool startup_off; // set before startup_init() for no startup screen (the simulator's older walk-throughs)

void startup_init(lv_obj_t *screen);  // after everything else on the screen, so it covers it
void startup_tick(bool have_layout); // every ui.c tick; a no-op once it has gone
