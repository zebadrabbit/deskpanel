// Gauge card (general, values() from /api/v1/cards): the title, then an arc filled to the value's fraction
// with the value inside, at its designed sizes:
//   1x2  a 76 px arc
//   2x3  a 100 px arc, like the GPU card's
// Green arc; amber/red arc, value and edge for warn/bad; greyed while stale; an empty arc and "—" with no value,
// the aggregator's reason dim at the right of the title row.
#include <stdlib.h>

#include "card_util.h"
#include "ellipse.h"

typedef struct {
    lv_obj_t *arc, *val, *why;
} gauge_t;

static void create(card_t *c)
{
    gauge_t *g = c->state = calloc(1, sizeof *g);
    bool big = c->h >= 3;
    int d = big ? 100 : 76, w = big ? 10 : 7;
    if (!big) { // 112 px tall: the title and a 76 px arc fit with slimmer padding and no gap
        lv_obj_set_style_pad_ver(c->box, 6, 0);
        lv_obj_set_style_pad_gap(c->box, 0, 0);
    }
    lv_obj_t *h = card_head(c->box);
    lv_obj_t *t = card_line(h, big ? font_text_20 : font_text_16, COL_DIM);
    lv_obj_set_flex_grow(t, 1);
    ui_text(t, c->def->title);
    g->why = card_line(h, font_text_16, COL_DIM);
    lv_obj_set_width(g->why, LV_SIZE_CONTENT);
    lv_obj_set_style_max_width(g->why, LV_PCT(60), 0);
    lv_obj_set_hidden(g->why, true);
    lv_obj_t *body = card_plain(c->box, LV_FLEX_FLOW_COLUMN, 0); // the arc, centred in the room left
    lv_obj_set_width(body, LV_PCT(100));
    lv_obj_set_flex_grow(body, 1);
    lv_obj_set_flex_align(body, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);

    g->arc = lv_arc_create(body);
    lv_obj_set_size(g->arc, d, d);
    lv_arc_set_range(g->arc, 0, 1000);
    lv_obj_remove_style(g->arc, NULL, LV_PART_KNOB);
    lv_obj_set_clickable(g->arc, false); // a tap on the arc is a tap on the card
    lv_obj_set_style_arc_width(g->arc, w, LV_PART_MAIN);
    lv_obj_set_style_arc_width(g->arc, w, LV_PART_INDICATOR);
    lv_obj_set_style_arc_color(g->arc, lv_color_hex(COL_EDGE), LV_PART_MAIN);
    ellipse_gauge(g->arc); // round on the glass
    g->val = card_line(g->arc, big ? font_num_28 : font_text_20, COL_TEXT);
    lv_obj_set_width(g->val, ellipse_w(d) - 2 * w); // inside the ring: a long value is cut, never drawn over it
    lv_obj_set_style_text_align(g->val, LV_TEXT_ALIGN_CENTER, 0);
    lv_obj_center(g->val);
}

static void values(card_t *c, const card_val_t *v, bool stale)
{
    gauge_t *g = c->state;
    uint32_t col = card_state(c, v, stale);
    bool none = col == COL_DIM;
    ellipse_gauge_set(g->arc, none ? 0 : (int32_t)(v->f * 1000 + 0.5f)); // f absent (-1): clamped to empty
    card_color(g->arc, LV_STYLE_ARC_COLOR, LV_PART_INDICATOR, none ? COL_EDGE : col == COL_TEXT ? COL_OK : col);
    card_color(g->val, LV_STYLE_TEXT_COLOR, LV_PART_MAIN, col);
    ui_text(g->val, card_value(v, false));
    bool why = none && v && v->sub[0];
    if (why) {
        ui_text(g->why, v->sub);
    }
    lv_obj_set_hidden(g->why, !why);
}

static void destroy(card_t *c) { free(c->state); }

const card_type_t card_gauge = {"gauge", create, NULL, destroy, values};
