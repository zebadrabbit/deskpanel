// Status card (general, values() from /api/v1/cards), 1x1 and 2x1: a 10 px dot in the state's colour and
// the title; when not ok the dim value word ("down") right after it. Red edge and dot when bad (amber for
// warn); greyed while stale, with a grey dot (the state is unknown); a grey dot and "—" with no value (or
// the aggregator's reason).
#include <stdlib.h>

#include "card_util.h"

typedef struct {
    lv_obj_t *dot, *title, *word;
    int32_t room, tw; // px for the title and the word; the whole title's width
} status_t;

static int32_t text_w(const char *text)
{
    lv_point_t sz;
    lv_text_get_size(&sz, text, font_text_16, 0, 0, LV_COORD_MAX, LV_TEXT_FLAG_NONE);
    return sz.x;
}

// The title gets what the word leaves (all of it when ok), but at least 60 %. A set width, not a max width:
// LVGL word-wraps a label's content size to its max width, which would cut "Quarterly Throughput" to "Quarterly...".
static void fit_title(status_t *s, const char *word, bool ok)
{
    int32_t want = LV_MIN(s->tw, ok ? s->room : LV_MAX(s->room * 6 / 10, s->room - text_w(word) - 6));
    if (lv_obj_get_style_width(s->title, 0) != want) {
        lv_obj_set_width(s->title, want);
    }
}

static void create(card_t *c)
{
    status_t *s = c->state = calloc(1, sizeof *s);
    int32_t w = lv_obj_get_style_width(c->box, 0); // place() sized it in px (a 4x6 card is the tab itself)
    int32_t pad = lv_obj_get_style_pad_left(c->box, 0) + lv_obj_get_style_pad_right(c->box, 0) +
                  2 * lv_obj_get_style_border_width(c->box, 0);
    s->room = (LV_COORD_IS_PX(w) ? w : 800) - pad - 10 - 8; // less the dot and its gap
    lv_obj_set_flex_flow(c->box, LV_FLEX_FLOW_ROW);
    lv_obj_set_flex_align(c->box, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
    lv_obj_set_style_pad_gap(c->box, 8, 0);
    s->dot = card_plain(c->box, LV_FLEX_FLOW_ROW, 0);
    lv_obj_set_size(s->dot, 10, 10);
    lv_obj_set_style_radius(s->dot, LV_RADIUS_CIRCLE, 0);
    lv_obj_set_style_bg_opa(s->dot, LV_OPA_COVER, 0);
    lv_obj_t *text = card_plain(c->box, LV_FLEX_FLOW_ROW, 6); // the title, and the word inline after it
    lv_obj_set_height(text, LV_SIZE_CONTENT);
    lv_obj_set_flex_grow(text, 1);
    s->title = card_line(text, font_text_16, COL_TEXT);
    ui_text(s->title, c->def->title);
    s->tw = text_w(c->def->title);
    s->word = card_line(text, font_text_16, COL_DIM);
    lv_obj_set_flex_grow(s->word, 1);
    fit_title(s, "", true);
}

static void values(card_t *c, const card_val_t *v, bool stale)
{
    status_t *s = c->state;
    card_state(c, v, stale);
    cv_state_t st = v ? v->st : CV_NONE;
    uint32_t dot = st == CV_OK ? COL_OK : st == CV_WARN ? COL_WARN : st == CV_BAD ? COL_BAD : COL_DIM;
    card_color(s->dot, LV_STYLE_BG_COLOR, LV_PART_MAIN, dot);
    lv_obj_set_hidden(s->word, st == CV_OK);
    ui_text(s->word, card_value(v, true));
    fit_title(s, card_value(v, true), st == CV_OK);
}

static void destroy(card_t *c) { free(c->state); }

const card_type_t card_status = {"status", create, NULL, destroy, values};
