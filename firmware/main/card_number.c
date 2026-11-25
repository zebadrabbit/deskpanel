// Number card (general: its value comes from /api/v1/cards through values()), at its designed sizes:
//   1x1  title left, value right, on one row
//   1x2  title, the big value, a dim sub-line
//   2x2  the same with a larger title and sub-line
// Amber/red edge and value for warn/bad, greyed while stale, "—" with no value (the reason goes in the
// sub-line, or at 1x1 in the value's place).
#include <stdlib.h>

#include "card_util.h"

typedef struct {
    lv_obj_t *val, *sub; // sub NULL at 1x1
} number_t;

static void create(card_t *c)
{
    number_t *n = c->state = calloc(1, sizeof *n);
    if (c->h == 1) {
        lv_obj_t *h = card_head(c->box);
        lv_obj_t *t = card_line(h, font_text_16, COL_DIM);
        lv_obj_set_flex_grow(t, 1);
        ui_text(t, c->def->title);
        n->val = card_line(h, font_text_20, COL_TEXT);
        lv_obj_set_width(n->val, LV_SIZE_CONTENT);
        lv_obj_set_style_max_width(n->val, LV_PCT(65), 0); // a long value leaves a long title some room
        lv_obj_set_flex_align(c->box, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_START);
        return;
    }
    const lv_font_t *small = c->w >= 2 ? font_text_20 : font_text_16;
    ui_text(card_line(c->box, small, COL_DIM), c->def->title);
    n->val = card_line(c->box, font_num_28, COL_TEXT);
    n->sub = card_line(c->box, small, COL_DIM);
}

static void values(card_t *c, const card_val_t *v, bool stale)
{
    number_t *n = c->state;
    card_color(n->val, LV_STYLE_TEXT_COLOR, LV_PART_MAIN, card_state(c, v, stale));
    ui_text(n->val, card_value(v, !n->sub));
    if (n->sub) {
        ui_text(n->sub, v ? v->sub : "");
    }
}

static void destroy(card_t *c) { free(c->state); }

const card_type_t card_number = {"number", create, NULL, destroy, values};
