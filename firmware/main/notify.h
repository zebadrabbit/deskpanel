// Notifications from the aggregator (POST /api/v1/notify on its side).
#pragma once

#include "lvgl.h"
#include "panel.h"

void notify_init(lv_obj_t *status_bar); // adds the bell to the status bar; LVGL task
void notify_update(const panel_t *p);   // on each new panel document; LVGL task
void notify_history(void);              // open the history sheet (the bell's tap); LVGL task
bool notify_popup_open(void);           // the newest note's pop-up is showing
