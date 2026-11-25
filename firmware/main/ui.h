#pragma once

#include "lvgl.h"

void ui_init(lv_display_t *disp); // call with the LVGL port lock held
void ui_tour_stop(void);          // ends the idle tour and closes the clock (however it was opened); LVGL task
