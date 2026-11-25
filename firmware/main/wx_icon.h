// Weather icons drawn from LVGL shapes (circles, lines, a bolt made of triangles): no image files.
#pragma once

#include <stdbool.h>

#include "lvgl.h"

lv_obj_t *wx_icon_create(lv_obj_t *parent, int size); // size px wide, size * 15/17 tall; blank until set
void wx_icon_set(lv_obj_t *icon, int wmo, bool day);  // WMO weather code; rebuilds only when the picture changes
