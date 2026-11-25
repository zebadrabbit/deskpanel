#include "card_util.h"

#include <stdio.h>
#include <string.h>

lv_obj_t *card_plain(lv_obj_t *parent, lv_flex_flow_t flow, int gap)
{
    lv_obj_t *o = lv_obj_create(parent);
    lv_obj_remove_style_all(o);
    lv_obj_set_flex_flow(o, flow);
    lv_obj_set_style_pad_gap(o, gap, 0);
    lv_obj_set_scrollable(o, false);
    lv_obj_set_clickable(o, false);
    return o;
}

void card_tile(lv_obj_t *t)
{
    lv_obj_set_style_bg_color(t, lv_color_hex(COL_TILE), 0);
    lv_obj_set_style_border_color(t, lv_color_hex(COL_EDGE), 0);
    lv_obj_set_style_border_width(t, 2, 0);
    lv_obj_set_style_radius(t, 10, 0);
    lv_obj_set_style_pad_all(t, 10, 0);
    lv_obj_set_style_pad_gap(t, 4, 0);
    lv_obj_set_scrollable(t, false);
    lv_obj_set_flex_flow(t, LV_FLEX_FLOW_COLUMN);
}

lv_obj_t *card_head(lv_obj_t *box)
{
    lv_obj_t *h = card_plain(box, LV_FLEX_FLOW_ROW, 8);
    lv_obj_set_size(h, LV_PCT(100), LV_SIZE_CONTENT);
    lv_obj_set_flex_align(h, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
    return h;
}

lv_obj_t *card_bar(lv_obj_t *parent, int h)
{
    lv_obj_t *b = lv_bar_create(parent);
    lv_obj_set_clickable(b, false); // a tap on the bar is a tap on the card
    lv_obj_set_size(b, LV_PCT(100), h);
    lv_obj_set_style_bg_color(b, lv_color_hex(COL_EDGE), LV_PART_MAIN);
    lv_obj_set_style_bg_opa(b, LV_OPA_COVER, LV_PART_MAIN);
    lv_obj_set_style_bg_color(b, lv_color_hex(COL_ACCENT), LV_PART_INDICATOR);
    return b;
}

void card_edge(lv_obj_t *box, uint32_t color) { card_color(box, LV_STYLE_BORDER_COLOR, LV_PART_MAIN, color); }

void card_color(lv_obj_t *o, lv_style_prop_t prop, lv_part_t part, uint32_t color)
{
    lv_color_t c = lv_color_hex(color);
    if (!lv_color_eq(lv_obj_get_style_prop(o, part, prop).color, c)) {
        lv_obj_set_local_style_prop(o, prop, (lv_style_value_t){.color = c}, part);
    }
}

uint32_t card_level(int pct, int warn, int bad, uint32_t normal)
{
    return pct >= bad ? COL_BAD : pct >= warn ? COL_WARN : normal;
}

void card_gib(char *buf, size_t n, int gib)
{
    if (gib >= 1024) {
        snprintf(buf, n, "%.1f TiB", gib / 1024.0);
    } else {
        snprintf(buf, n, "%d GiB", gib);
    }
}

int card_severity(const char *level)
{
    static const char *const order[] = {"WARNING", "ERROR", "CRITICAL", "ALERT", "EMERGENCY"};
    for (int i = 0; i < 5; i++) {
        if (!strcmp(level, order[i])) {
            return i + 1;
        }
    }
    return 0;
}

lv_obj_t *card_note(card_t *c)
{
    lv_obj_t *l = ui_label(c->box, font_text_16, COL_DIM);
    lv_obj_set_ignore_layout(l, true);
    lv_obj_set_width(l, LV_PCT(100));
    lv_obj_set_style_text_align(l, LV_TEXT_ALIGN_CENTER, 0);
    lv_label_set_long_mode(l, LV_LABEL_LONG_MODE_DOTS);
    lv_label_set_text(l, "\xe2\x80\x94"); // em dash: no data yet
    if (c->h == 1) { // one line, cut with "..."; taller cards wrap
        lv_obj_set_height(l, lv_font_get_line_height(font_text_16));
    }
    lv_obj_center(l);
    for (uint32_t i = 0; i + 1 < lv_obj_get_child_count(c->box); i++) { // the rest waits for the source
        lv_obj_set_hidden(lv_obj_get_child(c->box, (int32_t)i), true);
    }
    return l;
}

bool card_found(card_t *c, lv_obj_t *note, bool found, const char *kind)
{
    for (uint32_t i = 0; i < lv_obj_get_child_count(c->box); i++) {
        lv_obj_t *o = lv_obj_get_child(c->box, (int32_t)i);
        lv_obj_set_hidden(o, (o == note) == found);
    }
    if (!found) {
        char buf[48];
        snprintf(buf, sizeof buf, "no %s '%s'", kind, c->def->src);
        ui_text(note, buf);
    }
    return found;
}

void card_opa(card_t *c, bool stale, bool off)
{
    lv_opa_t o = (lv_opa_t)((off ? LV_OPA_50 : LV_OPA_COVER) * (stale ? LV_OPA_60 : LV_OPA_COVER) / LV_OPA_COVER);
    if (lv_obj_get_style_opa(c->box, 0) != o) {
        lv_obj_set_style_opa(c->box, o, 0);
    }
}

lv_obj_t *card_line(lv_obj_t *parent, const lv_font_t *font, uint32_t color)
{
    lv_obj_t *l = ui_label(parent, font, color);
    lv_obj_set_size(l, LV_PCT(100), lv_font_get_line_height(font));
    lv_label_set_long_mode(l, LV_LABEL_LONG_MODE_DOTS);
    return l;
}

uint32_t card_state(card_t *c, const card_val_t *v, bool stale)
{
    cv_state_t st = v ? v->st : CV_NONE;
    card_opa(c, stale || st == CV_STALE, false);
    card_edge(c->box, st == CV_WARN ? COL_WARN : st == CV_BAD ? COL_BAD : COL_EDGE);
    return st == CV_WARN ? COL_WARN : st == CV_BAD ? COL_BAD : st == CV_NONE ? COL_DIM : COL_TEXT;
}

const char *card_value(const card_val_t *v, bool reason)
{
    if (v && v->st != CV_NONE && v->v[0]) {
        return v->v;
    }
    return reason && v && v->sub[0] ? v->sub : "\xe2\x80\x94"; // em dash: no value
}

lv_obj_t *card_plot(lv_obj_t *parent, int32_t (*y)[CARD_PTS], int n)
{
    lv_obj_t *ch = lv_chart_create(parent);
    lv_obj_remove_style_all(ch); // no tile, border or point dots, and (below) no division lines
    lv_obj_set_width(ch, LV_PCT(100));
    lv_obj_set_flex_grow(ch, 1);
    lv_obj_set_style_pad_all(ch, 2, 0); // a 2 px line at 0 or 63, or at either end, is drawn whole
    lv_obj_set_style_line_width(ch, 2, LV_PART_ITEMS); // square ends: round ones overlap, beads when greyed
    lv_obj_set_style_size(ch, 0, 0, LV_PART_INDICATOR);
    lv_obj_set_clickable(ch, false); // a tap on the graph is a tap on the card
    lv_chart_set_div_line_count(ch, 0, 0);
    lv_chart_set_point_count(ch, CARD_PTS);
    lv_chart_set_axis_range(ch, LV_CHART_AXIS_PRIMARY_Y, 0, 63);
    for (int i = 0; i < n; i++) {
        for (int j = 0; j < CARD_PTS; j++) {
            y[i][j] = LV_CHART_POINT_NONE;
        }
        lv_color_t col = lv_color_hex(i ? COL_VIOLET : COL_ACCENT);
        lv_chart_set_series_ext_y_array(ch, lv_chart_add_series(ch, col, LV_CHART_AXIS_PRIMARY_Y), y[i]);
    }
    return ch;
}

bool card_points(int32_t *y, char *have, const char *s)
{
    if (!strcmp(s, have)) {
        return false;
    }
    snprintf(have, CARD_PTS + 1, "%s", s);
    int off = CARD_PTS - (int)strlen(have); // right-aligned
    for (int i = 0; i < CARD_PTS; i++) {
        int p = i < off ? -1 : cards_point(have[i - off]);
        y[i] = p < 0 ? LV_CHART_POINT_NONE : p; // outside the alphabet: skipped
    }
    return true;
}
