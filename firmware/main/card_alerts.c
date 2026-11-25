// NAS alerts card (no src): the count and level of WARNING-and-up alerts, "+N info" for the rest, an
// amber/red edge by severity (violet: info only). At 1x2 under a title; at 1x1 just the count in its
// coloured frame. Its default tap (alert-details) opens nas_alerts_sheet(): every undismissed alert in
// full, most severe first.
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "card_util.h"

typedef struct {
    lv_obj_t *info, *count;
} alerts_t;

static uint32_t level_color(const char *level)
{
    int s = card_severity(level);
    return s >= 2 ? COL_BAD : s == 1 ? COL_WARN : COL_ACCENT;
}

static void on_close(lv_event_t *e) { ui_sheet_close(); }

void nas_alerts_sheet(const panel_t *p)
{
    lv_obj_t *box = ui_sheet("NAS alerts");
    for (int i = 0; p && i < p->nalerts; i++) {
        lv_obj_t *row = card_plain(box, LV_FLEX_FLOW_ROW, 12);
        lv_obj_set_size(row, LV_PCT(100), LV_SIZE_CONTENT);
        lv_obj_t *lvl = ui_pill(row, level_color(p->alerts[i].level));
        lv_label_set_text(lvl, p->alerts[i].level);
        lv_obj_t *msg = ui_label(row, font_text_16, COL_TEXT);
        lv_obj_set_flex_grow(msg, 1);
        lv_label_set_long_mode(msg, LV_LABEL_LONG_MODE_WRAP);
        lv_label_set_text(msg, p->alerts[i].msg);
    }
    lv_obj_t *buttons = card_plain(box, LV_FLEX_FLOW_ROW, 0);
    lv_obj_set_size(buttons, LV_PCT(100), 64);
    ui_button(buttons, "Close", COL_EDGE, COL_TEXT, on_close);
}

static void create(card_t *c)
{
    alerts_t *a = c->state = calloc(1, sizeof *a);
    lv_obj_t *h = card_head(c->box);
    lv_obj_t *title = ui_label(h, &lv_font_montserrat_16, COL_DIM);
    lv_label_set_text(title, "NAS ALERTS");
    lv_obj_set_flex_grow(title, 1);
    a->info = ui_label(h, &lv_font_montserrat_16, COL_VIOLET);
    a->count = ui_label(c->box, &lv_font_montserrat_20, COL_TEXT);
    lv_label_set_text(a->count, "--"); // no data yet (montserrat_20 has no em dash)
    if (!strcmp(c->def->tap, "alert-details")) {
        lv_obj_set_clickable(c->box, false); // until there are alerts to show
    }
    if (c->h < 2) { // 1x1: the count alone, vertically centred
        lv_obj_set_hidden(h, true);
        lv_obj_set_flex_align(c->box, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_START);
    }
}

static void update(card_t *c, const panel_t *p)
{
    alerts_t *a = c->state;
    char buf[32];
    int severe = 0; // WARNING and up; INFO/NOTICE only show in the sheet and the "+N info" count
    for (int i = 0; i < p->nalerts; i++) {
        severe += card_severity(p->alerts[i].level) > 0;
    }
    card_opa(c, p->homelab_stale, false);
    snprintf(buf, sizeof buf, "+%d info", p->info_alerts);
    ui_text(a->info, p->info_alerts ? buf : "");
    int sev = severe ? card_severity(p->alerts[0].level) : 0; // the aggregator sorts most severe first
    uint32_t col = sev >= 2 ? COL_BAD : sev == 1 ? COL_WARN : COL_EDGE;
    snprintf(buf, sizeof buf, "%d  %s", severe, p->alerts[0].level);
    ui_text(a->count, severe ? buf : "All clear");
    lv_obj_set_style_text_color(a->count, lv_color_hex(severe ? col : COL_OK), 0);
    if (!strcmp(c->def->tap, "alert-details")) {
        lv_obj_set_clickable(c->box, p->nalerts > 0); // tap for the full text
    }
    card_edge(c->box, severe ? col : p->info_alerts ? COL_VIOLET : COL_EDGE); // violet: info only
}

static void destroy(card_t *c) { free(c->state); }

const card_type_t card_alerts = {"nas-alerts", create, update, destroy, NULL};
