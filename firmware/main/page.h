// The screen's shared look and helpers (ui.c), for the cards (card.h), notifications and the takeover.
// Tabs come from the layout. All on the LVGL task.
#pragma once

#include "lvgl.h"
#include "panel.h"

void ui_show_tab(const char *name);              // switch to the layout's tab of that name, animated; NULL: no-op
// The tab holding the first card of that type, or NULL. The name points into the layout on screen: valid
// only until the next layout change (a rebuild), so copy it to keep it.
const char *ui_tab_of_type(const char *type);

// Shared look (GitHub-dark palette) and helpers, defined in ui.c.
#define COL_BG     0x0d1117
#define COL_TILE   0x161b22
#define COL_EDGE   0x30363d
#define COL_TEXT   0xe6edf3
#define COL_DIM    0x8b949e
#define COL_OK     0x3fb950
#define COL_WARN   0xd29922
#define COL_BAD    0xf85149
#define COL_ACCENT 0x58a6ff
#define COL_VIOLET 0xbc8cff

lv_obj_t *ui_label(lv_obj_t *parent, const lv_font_t *font, uint32_t color);
lv_obj_t *ui_pill(lv_obj_t *parent, uint32_t color); // small rounded badge
void ui_text(lv_obj_t *label, const char *text);    // only redraws when the text changed
void ui_toast(const char *text, uint32_t color);    // brief message above the tab bar (or the takeover notice)
lv_obj_t *ui_button(lv_obj_t *parent, const char *text, uint32_t bg, uint32_t fg, lv_event_cb_t cb);

// One modal sheet at a time, on the top layer: returns its (scrollable) content box under `title`.
// A tap outside the box closes it.
lv_obj_t *ui_sheet(const char *title);
void ui_sheet_close(void);
bool ui_sheet_is_open(void);

// Montserrat rendered from the embedded TTF (Tiny TTF), so user text with em dashes, curly quotes
// and accents draws; LV_SYMBOL_* glyphs fall back to the built-in fonts. Ready before create().
// 14 / 17 / 22 are the weather mockup's sizes (17 and 22 shared with the astronomical clock).
extern const lv_font_t *font_text_12, *font_text_16, *font_text_20;
extern const lv_font_t *font_text_14, *font_text_17, *font_text_22;
// Card values at 28 px: the built-in Montserrat 28 (no RAM), with a 28 px Tiny TTF behind it for the glyphs it
// lacks (the em dash of a card with no value, a unit's accents), so only those are rendered and cached.
extern const lv_font_t *font_num_28;
// Weather's big numbers: Montserrat ExtraBold 120 px and SemiBold 26 px, ASCII + ° · − only.
extern const lv_font_t *font_bold_120, *font_semi_26;
