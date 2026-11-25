// The full-screen astronomical clock (astro_view.c): a night-sky dial and an information column over the
// status bar and the tabs, under the top layer (so the takeover notice, toasts and sheets stay above it). A tap on it
// closes it. All on the LVGL task.
#pragma once

#include <stdbool.h>

#include "lvgl.h"

void astro_view_init(lv_obj_t *screen); // creates every object once, hidden, as the screen's last child (A7)
void astro_view_open(bool fade);        // fade: over about 300 ms
void astro_view_close(bool fade);
bool astro_view_is_open(void);          // false from the moment a close starts
void astro_view_tick(void);             // from ui.c's tick: takes new data; moves the hands when the minute changes
