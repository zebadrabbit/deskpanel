// Pool card (src: a ZFS pool name), at its designed sizes:
//   1x2  name and health, the usage bar, used / size
//   2x2  the same with used and free in large type
//   1x1  name and used %
// Amber/red edge, bar and % as the pool fills, red edge when it isn't healthy.
#include <stdio.h>
#include <stdlib.h>

#include "card_util.h"

typedef struct {
    lv_obj_t *name, *health, *bar, *usage, *used, *avail, *pct, *note; // NULL where the size has none
} pool_t;

// 2x2: a big number and its dim word, on one baseline.
static lv_obj_t *amount(lv_obj_t *row, const char *word)
{
    lv_obj_t *a = card_plain(row, LV_FLEX_FLOW_ROW, 6);
    lv_obj_set_size(a, LV_SIZE_CONTENT, LV_SIZE_CONTENT);
    lv_obj_set_flex_align(a, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_END, LV_FLEX_ALIGN_END);
    lv_obj_t *n = ui_label(a, &lv_font_montserrat_28, COL_TEXT);
    lv_obj_t *w = ui_label(a, &lv_font_montserrat_16, COL_DIM);
    lv_label_set_text(w, word);
    lv_obj_set_style_pad_bottom(w, 2, 0); // the fonts' baselines sit 5 and 3 px up
    return n;
}

static void create(card_t *c)
{
    pool_t *t = c->state = calloc(1, sizeof *t);
    lv_obj_t *h = card_head(c->box);
    t->name = ui_label(h, &lv_font_montserrat_16, COL_DIM);
    lv_obj_set_flex_grow(t->name, 1);
    lv_label_set_long_mode(t->name, LV_LABEL_LONG_MODE_DOTS);
    if (c->h == 1) { // one line, cut with "..." (taller cards let a long name wrap, as they always have)
        lv_obj_set_height(t->name, lv_font_get_line_height(&lv_font_montserrat_16));
        t->pct = ui_label(h, &lv_font_montserrat_20, COL_TEXT);
        lv_obj_set_flex_align(c->box, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_START);
        t->note = card_note(c);
        return;
    }
    t->health = ui_label(h, &lv_font_montserrat_16, COL_OK);
    t->bar = card_bar(c->box, 12);
    if (c->w == 1) {
        t->usage = ui_label(c->box, &lv_font_montserrat_20, COL_TEXT);
    } else {
        lv_obj_t *row = card_plain(c->box, LV_FLEX_FLOW_ROW, 0);
        lv_obj_set_size(row, LV_PCT(100), LV_SIZE_CONTENT);
        lv_obj_set_style_pad_top(row, 4, 0);
        lv_obj_set_flex_align(row, LV_FLEX_ALIGN_SPACE_BETWEEN, LV_FLEX_ALIGN_END, LV_FLEX_ALIGN_END);
        t->used = amount(row, "used");
        t->avail = amount(row, "free");
    }
    t->note = card_note(c);
}

static void update(card_t *c, const panel_t *p)
{
    pool_t *t = c->state;
    const panel_pool_t *d = panel_pool(p, c->def->src);
    card_opa(c, p->homelab_stale, false);
    if (!card_found(c, t->note, d != NULL, "pool")) {
        card_edge(c->box, COL_EDGE);
        return;
    }
    int total = d->used_gib + d->avail_gib;
    int pct = total > 0 ? d->used_gib * 100 / total : 0;
    char used[16], size[16], buf[48];
    card_gib(used, sizeof used, d->used_gib);
    card_gib(size, sizeof size, total);
    ui_text(t->name, d->name);
    if (t->pct) {
        snprintf(buf, sizeof buf, "%d%%", pct);
        ui_text(t->pct, buf);
        lv_obj_set_style_text_color(t->pct, lv_color_hex(card_level(pct, FULL_WARN, FULL_BAD, COL_TEXT)), 0);
    } else {
        ui_text(t->health, d->health);
        lv_obj_set_style_text_color(t->health, lv_color_hex(d->ok ? COL_OK : COL_BAD), 0);
        lv_bar_set_value(t->bar, pct, LV_ANIM_OFF);
        lv_obj_set_style_bg_color(t->bar, lv_color_hex(card_level(pct, FULL_WARN, FULL_BAD, COL_ACCENT)), LV_PART_INDICATOR);
    }
    if (t->usage) {
        snprintf(buf, sizeof buf, "%s / %s", used, size);
        ui_text(t->usage, buf);
    }
    if (t->used) {
        ui_text(t->used, used);
        card_gib(buf, sizeof buf, d->avail_gib);
        ui_text(t->avail, buf);
    }
    card_edge(c->box, !d->ok ? COL_BAD : card_level(pct, FULL_WARN, FULL_BAD, COL_EDGE));
}

static void destroy(card_t *c) { free(c->state); }

const card_type_t card_pool = {"pool", create, update, destroy, NULL};
