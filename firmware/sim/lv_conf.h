// LVGL configuration for the PC simulator only; the ESP build configures LVGL through sdkconfig.
// Anything not set here takes LVGL's default. Fonts, Tiny TTF and the dark theme match the panel.
#ifndef LV_CONF_H
#define LV_CONF_H

#define LV_COLOR_DEPTH        32
#define LV_USE_STDLIB_MALLOC  LV_STDLIB_CLIB
#define LV_USE_STDLIB_STRING  LV_STDLIB_CLIB
#define LV_USE_STDLIB_SPRINTF LV_STDLIB_CLIB
#define LV_USE_OS             LV_OS_NONE

#define LV_FONT_MONTSERRAT_12 1
#define LV_FONT_MONTSERRAT_14 1
#define LV_FONT_MONTSERRAT_16 1
#define LV_FONT_MONTSERRAT_20 1
#define LV_FONT_MONTSERRAT_28 1
#define LV_FONT_DEFAULT       &lv_font_montserrat_20
#define LV_USE_TINY_TTF       1
#define LV_TINY_TTF_CACHE_GLYPH_CNT 256
#define LV_THEME_DEFAULT_DARK 1

#define LV_USE_LOG            1
#define LV_LOG_LEVEL          LV_LOG_LEVEL_WARN
#define LV_LOG_PRINTF         1

#endif
