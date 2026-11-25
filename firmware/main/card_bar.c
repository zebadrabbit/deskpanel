// Bar card (general, values() from /api/v1/cards): the value's fraction as a bar, the value as text, at its
// designed sizes:
//   1x1  title and value on one row, a thin bar under them
//   1x2  a title row with the value, a 12 px bar, then the sub-line
//   2x1  one row: title, the bar (it grows), the value
// Blue bar and dim value; amber/red for warn/bad, with the edge; greyed while stale; an empty bar and "—" with
// no value (the reason goes in the sub-line, or where there's none in the value's place).
#include <stdlib.h>

#include "card_util.h"

typedef struct {
    lv_obj_t *bar, *val, *sub; // sub NULL but at 1x2
} bar_t;

static void create(card_t *c)
{
    bar_t *b = c->state = calloc(1, sizeof *b);
    lv_obj_t *h = card_head(c->box);
    lv_obj_t *t = card_line(h, font_text_16, COL_DIM);
    ui_text(t, c->def->title);
    if (c->w >= 2) { // 2x1
        lv_obj_set_width(t, LV_SIZE_CONTENT);
        lv_obj_set_style_max_width(t, LV_PCT(40), 0);
        b->bar = card_bar(h, 10);
        lv_obj_set_flex_grow(b->bar, 1);
    } else {
        lv_obj_set_flex_grow(t, 1);
    }
    b->val = card_line(h, font_text_16, COL_DIM);
    lv_obj_set_width(b->val, LV_SIZE_CONTENT);
    lv_obj_set_style_max_width(b->val, LV_PCT(c->w >= 2 ? 30 : 60), 0);
    if (c->h >= 2 && !b->bar) { // 1x2
        b->bar = card_bar(c->box, 12);
        lv_obj_set_style_margin_ver(b->bar, 3, 0);
        b->sub = card_line(c->box, font_text_20, COL_TEXT);
    } else if (!b->bar) { // 1x1: room for the row and a thin bar with slimmer padding
        b->bar = card_bar(c->box, 6);
        lv_obj_set_style_pad_ver(c->box, 6, 0);
    }
    lv_bar_set_range(b->bar, 0, 1000);
    if (c->h == 1) {
        lv_obj_set_flex_align(c->box, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_START);
    }
}

static void values(card_t *c, const card_val_t *v, bool stale)
{
    bar_t *b = c->state;
    uint32_t col = card_state(c, v, stale);
    bool none = col == COL_DIM;
    lv_bar_set_value(b->bar, none ? 0 : (int32_t)(v->f * 1000 + 0.5f), LV_ANIM_OFF); // f absent (-1): empty
    card_color(b->bar, LV_STYLE_BG_COLOR, LV_PART_INDICATOR, col == COL_TEXT || none ? COL_ACCENT : col);
    card_color(b->val, LV_STYLE_TEXT_COLOR, LV_PART_MAIN, col == COL_TEXT ? COL_DIM : col);
    ui_text(b->val, card_value(v, !b->sub));
    if (b->sub) {
        card_color(b->sub, LV_STYLE_TEXT_COLOR, LV_PART_MAIN, none ? COL_DIM : COL_TEXT); // a reason, not a value
        ui_text(b->sub, v ? v->sub : "");
    }
}

static void destroy(card_t *c) { free(c->state); }

const card_type_t card_bars = {"bar", create, NULL, destroy, values};
