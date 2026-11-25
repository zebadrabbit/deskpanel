// Graph card (general, values() from /api/v1/cards): the last hour of one or two values (S4: up to 60 points,
// one base64url character each, oldest first, a short series right-aligned), at its designed sizes:
//   2x2, 4x2  the title, the value at the right of its row, the plot under them
//   2x3, 4x3  the title with "last hour" at its right, the big value, the plot
// Line 1 accent blue, line 2 violet, 2 px, over a dashed amber threshold at th. Two values read "↓ v" and "↑ v2"
// in the line colours. Amber/red edge and value for warn/bad; greyed while stale; "—" (or the aggregator's
// reason) over an empty plot with no value. The points change only when a line's string does: the document
// comes every 5 s, the points every 60 s, and an unchanged graph costs no redraw.
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "card_util.h"

typedef struct {
    lv_obj_t *val[2], *note, *chart; // note: "last hour" at 2x3 and 4x3, else NULL
    int32_t y[2][CARD_PTS];  // the chart draws straight from these
    char g[2][CARD_PTS + 1]; // the strings they hold
    int th;             // the threshold point, -1 for none
} graph_t;

// Draws the threshold before the chart draws its lines, so they pass over it.
static void draw_th(lv_event_t *e)
{
    const graph_t *g = lv_event_get_user_data(e);
    if (g->th < 0) {
        return;
    }
    lv_area_t a;
    lv_obj_get_content_coords(lv_event_get_current_target(e), &a);
    lv_draw_line_dsc_t d;
    lv_draw_line_dsc_init(&d);
    d.color = lv_color_hex(COL_WARN);
    d.opa = LV_OPA_60;
    d.width = 1;
    d.dash_width = d.dash_gap = 4;
    d.p1.x = a.x1; // the chart's points run from x1 to x2 + 1, y2 + 1 (0) to y1 (63)
    d.p2.x = a.x2 + 1;
    d.p1.y = d.p2.y = lv_map(g->th, 0, 63, a.y2 + 1, a.y1);
    lv_draw_line(lv_event_get_layer(e), &d);
}

static void create(card_t *c)
{
    graph_t *g = c->state = calloc(1, sizeof *g);
    g->th = -1;
    bool big = c->h >= 3;
    lv_obj_t *row = card_head(c->box);
    lv_obj_t *t = card_line(row, font_text_16, COL_DIM);
    lv_obj_set_flex_grow(t, 1);
    ui_text(t, c->def->title);
    if (big) {
        g->note = card_line(row, font_text_16, COL_DIM);
        lv_obj_set_width(g->note, LV_SIZE_CONTENT);
        lv_obj_set_style_max_width(g->note, LV_PCT(65), 0);
        row = card_head(c->box); // the big value
        lv_obj_set_style_pad_gap(row, 20, 0); // two values stay apart
    }
    for (int i = 0; i < 2; i++) { // two values: neither may take the title's room at 2x2 (values() sets val[0]'s)
        g->val[i] = card_line(row, big ? font_num_28 : font_text_16, COL_DIM);
        lv_obj_set_width(g->val[i], LV_SIZE_CONTENT);
        lv_obj_set_style_max_width(g->val[i], LV_PCT(big ? 46 : 45), 0);
    }

    g->chart = card_plot(c->box, g->y, 2);
    lv_obj_add_event_cb(g->chart, draw_th, LV_EVENT_DRAW_MAIN_BEGIN, g);
}

static void values(card_t *c, const card_val_t *v, bool stale)
{
    graph_t *g = c->state;
    uint32_t col = card_state(c, v, stale);
    bool two = col != COL_DIM && v->v2[0];
    char buf[24];
    if (two) {
        snprintf(buf, sizeof buf, "\xe2\x86\x93 %s", v->v); // ↓
        ui_text(g->val[0], buf);
        snprintf(buf, sizeof buf, "\xe2\x86\x91 %s", v->v2); // ↑
        ui_text(g->val[1], buf);
    } else {
        ui_text(g->val[0], card_value(v, !g->note));
    }
    lv_obj_set_hidden(g->val[1], !two);
    int32_t mw = LV_PCT(two ? (g->note ? 46 : 45) : g->note ? 100 : 65); // one value may take what the title leaves
    if (lv_obj_get_style_max_width(g->val[0], 0) != mw) {
        lv_obj_set_style_max_width(g->val[0], mw, 0);
    }
    uint32_t dim = col == COL_TEXT ? COL_DIM : col; // the small text: dim unless warn or bad
    card_color(g->val[0], LV_STYLE_TEXT_COLOR, LV_PART_MAIN, two ? COL_ACCENT : g->note ? col : dim);
    card_color(g->val[1], LV_STYLE_TEXT_COLOR, LV_PART_MAIN, COL_VIOLET);
    if (g->note) {
        ui_text(g->note, col == COL_DIM && v && v->sub[0] ? v->sub : "last hour");
        card_color(g->note, LV_STYLE_TEXT_COLOR, LV_PART_MAIN, dim);
    }

    bool dirty = false;
    for (int k = 0; k < 2; k++) {
        dirty |= card_points(g->y[k], g->g[k], v && k < v->ng ? v->g[k] : "");
    }
    int th = v ? v->th : -1;
    if (th != g->th) {
        g->th = th;
        dirty = true;
    }
    if (dirty) {
        lv_chart_refresh(g->chart);
    }
}

static void destroy(card_t *c) { free(c->state); }

const card_type_t card_graph = {"graph", create, NULL, destroy, values};
