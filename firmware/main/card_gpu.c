// GPU card (src: a GPU host, "host#2" for its second GPU), at its designed sizes (the approved sizes.html):
//   2x3  host, model and state pill; the gauge beside line 1 (its text over a bar or graph) and line 2
//   2x2  the same with a big number in place of the gauge; under a graph line 2 gives it its room
//   1x3  host and pill; the gauge above line 1 (a smaller gauge over a graph)
//   1x2  like a pool tile: host and state, the big number, line 1's bar (a graph slot's too; power, which has no
//        bar, its text) or its text
//   1x1  host and the gauge's number
// The slots (S2, from the layout; none, from an older aggregator, is today's card): the gauge shows temp, util,
// VRAM % or fan; line 1 is VRAM, util, temp, power or fan as text over a bar, a graph or nothing; line 2 joins up
// to two of temp, util, VRAM, power, procs and fan. The text comes from /panel (update); a graph's points and the
// fan (J7: "45%" and 0.45, or "--") come from /api/v1/cards (values). Each keeps its part in the state and draws
// the card from both, so whichever came last wins. Amber/red edge on a hot GPU (and the temp number, where
// there's no gauge); dimmed while the agent is offline.
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "card_util.h"
#include "ellipse.h"

enum { TEMP, UTIL, VRAM, POWER, PROCS, FAN }; // slot values

typedef struct {
    // num: the gauge's number; line1/line2: the slots' text. NULL where the size or the slots have none.
    lv_obj_t *host, *model, *state, *arc, *num, *line1, *bar, *chart, *line2, *note;
    int gauge, v1, v2[2], n2; // the slots' values
    int32_t y[1][CARD_PTS];   // line 1's graph
    char g[CARD_PTS + 1];
    bool seen, found, stale; // update: a /panel document came; it has this GPU (d, a copy); homelab_stale
    panel_gpu_t d;
    bool shows_fan; // a slot shows the fan
    bool fstale;    // values: the cards feed is stale, or this entry says so: the fan's own objects grey (C2a)
    char fv[16];    // values: the fan's text ("45%")
    float ff;       // values: its fraction, -1 when unknown
} gpu_t;

static void fade(lv_obj_t *o, bool on) // opacity 60 while on; set only when it changes
{
    lv_opa_t a = on ? LV_OPA_60 : LV_OPA_COVER;
    if (o && lv_obj_get_style_opa(o, 0) != a) {
        lv_obj_set_style_opa(o, a, 0);
    }
}

static bool pill(const card_t *c) { return c->w >= 2 || c->h >= 3; } // 1x2 has plain state text, 1x1 none

static int slot(const char *s, int none) // a slot value's name, or `none` when it's empty or unknown
{
    static const char *const names[] = {"temp", "util", "vram", "power", "procs", "fan"};
    for (int i = 0; i < 6; i++) {
        if (!strcmp(s, names[i])) {
            return i;
        }
    }
    return none;
}

static void create(card_t *c)
{
    gpu_t *g = c->state = calloc(1, sizeof *g);
    const layout_card_t *d = c->def;
    bool sent = d->gauge[0] || d->line1[0] || d->as[0]; // else the S2 defaults: today's card
    g->gauge = slot(d->gauge, TEMP);
    g->v1 = slot(d->line1, VRAM);
    for (int i = 0; i < 2; i++) {
        int v = sent ? slot(d->line2[i], -1) : i ? POWER : UTIL;
        if (v >= 0) {
            g->v2[g->n2++] = v;
        }
        g->shows_fan |= v == FAN;
    }
    g->shows_fan |= g->gauge == FAN || g->v1 == FAN;
    g->ff = -1;
    // What line 1 draws under its text at this size: 'g'raph (2x3, 2x2, 1x3: it needs 40 px), 'b'ar or 't'ext only.
    char as = !strcmp(d->as, "text") ? 't' : !strcmp(d->as, "graph") && pill(c) ? 'g' : 'b';
    if (as == 'b' && g->v1 == POWER) {
        as = 't'; // power has no bar
    }
    bool wide = c->w >= 2;
    lv_obj_t *h = card_head(c->box);
    g->host = ui_label(h, wide ? &lv_font_montserrat_20 : &lv_font_montserrat_16, COL_TEXT);
    if (wide) {
        g->model = ui_label(h, &lv_font_montserrat_16, COL_DIM);
    }
    lv_obj_t *give = wide ? g->model : g->host; // takes the room left, cut with "..." on one line
    lv_obj_set_flex_grow(give, 1);
    lv_label_set_long_mode(give, LV_LABEL_LONG_MODE_DOTS);
    lv_obj_set_height(give, lv_font_get_line_height(lv_obj_get_style_text_font(give, 0)));
    if (c->h == 1) { // 1x1
        g->num = ui_label(h, &lv_font_montserrat_20, COL_TEXT);
        lv_obj_set_flex_align(c->box, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_START);
        g->note = card_note(c);
        return;
    }
    g->state = pill(c) ? ui_pill(h, COL_DIM) : ui_label(h, &lv_font_montserrat_16, COL_DIM);
    if (!wide && pill(c)) {
        lv_obj_set_style_pad_hor(g->state, 6, 0); // 1x3: a slimmer pill leaves "storage" whole beside LOADED
    }
    if (!pill(c)) { // 1x2
        g->num = ui_label(c->box, &lv_font_montserrat_28, COL_TEXT);
        if (as == 'b') {
            g->bar = card_bar(c->box, 12);
        } else {
            g->line1 = card_line(c->box, &lv_font_montserrat_16, COL_DIM);
        }
        g->note = card_note(c);
        return;
    }

    // 2x3, 2x2: a row of the gauge (or number) and the lines; 1x3: the gauge, centred in the room left (over a
    // graph, as small as leaves the graph its 40 px)
    bool small = !wide && as == 'g';
    lv_obj_t *body = card_plain(c->box, wide ? LV_FLEX_FLOW_ROW : LV_FLEX_FLOW_COLUMN, 16);
    lv_obj_set_width(body, LV_PCT(100));
    if (small) {
        lv_obj_set_height(body, LV_SIZE_CONTENT);
    } else {
        lv_obj_set_flex_grow(body, 1);
    }
    lv_obj_set_flex_align(body, wide ? LV_FLEX_ALIGN_START : LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER,
                          LV_FLEX_ALIGN_CENTER);
    if (c->h >= 3) {
        // The number fits inside the ring: 28 pt "100%" only in the 2x3's, "44°" in the 1x3's, 16 pt in the small one.
        static const lv_font_t *const fonts[] = {&lv_font_montserrat_16, &lv_font_montserrat_20, &lv_font_montserrat_28};
        // small: round on the glass it's 51 px wide, and a 3 px ring leaves "100%" (41 px) 45
        int sz = wide ? 100 : small ? 54 : 80, w = small ? 3 : 10;
        g->arc = lv_arc_create(body);
        lv_obj_set_size(g->arc, sz, sz);
        lv_arc_set_range(g->arc, 0, 100);
        lv_obj_remove_style(g->arc, NULL, LV_PART_KNOB);
        lv_obj_set_clickable(g->arc, false);
        lv_obj_set_style_arc_width(g->arc, w, LV_PART_MAIN);
        lv_obj_set_style_arc_width(g->arc, w, LV_PART_INDICATOR);
        lv_obj_set_style_arc_color(g->arc, lv_color_hex(COL_EDGE), LV_PART_MAIN);
        ellipse_gauge(g->arc); // round on the glass
        g->num = ui_label(g->arc, fonts[2 - small - (!wide && g->gauge != TEMP)], COL_TEXT);
        lv_obj_center(g->num);
    } else {
        g->num = ui_label(body, &lv_font_montserrat_28, COL_TEXT);
    }

    lv_obj_t *col = c->box; // 1x3: line 1 under the gauge
    bool l2 = wide && g->n2 && (c->h >= 3 || as != 'g'); // 2x2: a graph takes line 2's room
    if (wide) {
        col = card_plain(body, LV_FLEX_FLOW_COLUMN, c->h >= 3 ? 8 : as == 'g' ? 2 : 4);
        // Over a graph, as tall as the gauge (2x3) or the body (2x2): the plot takes what the text leaves.
        lv_obj_set_height(col, as != 'g' ? LV_SIZE_CONTENT : c->h >= 3 ? 100 : LV_PCT(100));
        lv_obj_set_flex_grow(col, 1);
    }
    g->line1 = ui_label(col, &lv_font_montserrat_16, COL_DIM);
    if (as == 'b') {
        g->bar = card_bar(col, c->h >= 3 ? (wide ? 14 : 12) : 10);
    } else if (as == 'g') {
        g->chart = card_plot(col, g->y, 1);
    }
    if (l2) {
        g->line2 = ui_label(col, &lv_font_montserrat_20, COL_TEXT);
    }
    g->note = card_note(c);
}

// The card from the state update() and values() keep. Every setter is a no-op when nothing changed.
static void draw(card_t *c)
{
    gpu_t *g = c->state;
    const panel_gpu_t *d = g->found ? &g->d : NULL;
    if (!card_found(c, g->note, d != NULL, "gpu")) {
        card_opa(c, g->stale, false);
        card_edge(c->box, COL_EDGE);
        return;
    }
    char buf[48];
    ui_text(g->host, d->host);
    if (g->model) {
        ui_text(g->model, d->up ? d->name : "agent offline");
    }
    card_opa(c, g->stale, !d->up);
    // The fan: unknown with no reading, from an old agent, or while the agent is offline (J7).
    bool fk = d->up && g->ff >= 0;
    int fan = fk ? (int)(g->ff * 100 + 0.5f) : 0;
    const char *ft = fk ? g->fv : "--";

    if (g->state) {
        uint32_t sc = !strcmp(d->state, "busy") ? COL_ACCENT : !strcmp(d->state, "loaded") ? COL_VIOLET : COL_DIM;
        snprintf(buf, sizeof buf, "%s", d->state);
        for (char *ch = buf; *ch; ch++) {
            *ch = (char)(*ch >= 'a' && *ch <= 'z' ? *ch - 32 : *ch); // badge text in caps
        }
        ui_text(g->state, buf);
        card_color(g->state, pill(c) ? LV_STYLE_BG_COLOR : LV_STYLE_TEXT_COLOR, LV_PART_MAIN, sc);
    }

    int pct = d->vram_total > 0 ? d->vram * 100 / d->vram_total : 0;
    int gv = g->gauge == UTIL ? d->util : g->gauge == VRAM ? pct : g->gauge == FAN ? fan : d->temp; // °C or %
    bool hot = g->gauge == TEMP; // only the temperature has levels
    uint32_t tc = card_level(d->temp, TEMP_WARN, TEMP_BAD, COL_ACCENT);
    if (g->arc) {
        ellipse_gauge_set(g->arc, gv < 0 ? 0 : gv > 100 ? 100 : gv);
        card_color(g->arc, LV_STYLE_ARC_COLOR, LV_PART_INDICATOR, hot ? tc : COL_ACCENT);
    } else { // no gauge: the number shows the heat
        card_color(g->num, LV_STYLE_TEXT_COLOR, LV_PART_MAIN, d->up && hot ? card_level(d->temp, TEMP_WARN, TEMP_BAD, COL_TEXT) : COL_TEXT);
    }
    snprintf(buf, sizeof buf, hot ? "%d°" : "%d%%", gv);
    ui_text(g->num, (g->gauge == FAN ? fk : d->up) ? buf : "--");
    card_edge(c->box, d->up && d->temp >= TEMP_WARN ? tc : COL_EDGE);
    // C2a: stale cards data greys the fan's own objects (the number is the ring's child); line 2 mixes values.
    fade(g->arc ? g->arc : g->num, g->gauge == FAN && g->fstale);
    fade(g->line1, g->v1 == FAN && g->fstale);
    fade(g->bar, g->v1 == FAN && g->fstale);

    if (g->bar) {
        lv_bar_set_value(g->bar, g->v1 == UTIL ? d->util : g->v1 == TEMP ? d->temp : g->v1 == FAN ? fan : pct, LV_ANIM_OFF);
    }
    if (g->line1) {
        const char *off = g->v1 == UTIL ? "Util --" : g->v1 == TEMP ? "Temp --" : g->v1 == POWER ? "Power --" : NULL;
        if (!d->up && off) { // no false zeros from an offline agent (VRAM keeps today's text)
            snprintf(buf, sizeof buf, "%s", off);
        } else if (g->v1 == UTIL) {
            snprintf(buf, sizeof buf, "Util %d %%", d->util);
        } else if (g->v1 == TEMP) {
            snprintf(buf, sizeof buf, "Temp %d°", d->temp);
        } else if (g->v1 == POWER) {
            snprintf(buf, sizeof buf, "Power %d W", d->power);
        } else if (g->v1 == FAN) {
            snprintf(buf, sizeof buf, "Fan %s", ft);
        } else {
            snprintf(buf, sizeof buf, "VRAM  %.1f / %.0f GB", d->vram / 1024.0, d->vram_total / 1024.0);
        }
        ui_text(g->line1, buf);
    }
    if (g->line2) { // "67%  •  163 W"
        char a[2][20] = {"", ""};
        for (int i = 0; i < g->n2; i++) {
            int v = g->v2[i];
            if (v == UTIL) {
                snprintf(a[i], sizeof a[i], "%d%%", d->util);
            } else if (v == TEMP) {
                snprintf(a[i], sizeof a[i], "%d°", d->temp);
            } else if (v == VRAM) {
                snprintf(a[i], sizeof a[i], "%.1f / %.0f GB", d->vram / 1024.0, d->vram_total / 1024.0);
            } else if (v == PROCS) {
                snprintf(a[i], sizeof a[i], "%d proc%s", d->procs, d->procs == 1 ? "" : "s");
            } else if (v == FAN) {
                snprintf(a[i], sizeof a[i], "%s fan", ft);
            } else {
                snprintf(a[i], sizeof a[i], "%d W", d->power);
            }
        }
        snprintf(buf, sizeof buf, "%s%s%s", a[0], g->n2 > 1 ? "  " LV_SYMBOL_BULLET "  " : "", g->n2 > 1 ? a[1] : "");
        ui_text(g->line2, d->up ? buf : "");
    }
}

static void update(card_t *c, const panel_t *p)
{
    gpu_t *g = c->state;
    const panel_gpu_t *d = panel_gpu(p, c->def->src);
    g->seen = true;
    g->found = d != NULL;
    if (d) {
        g->d = *d;
    }
    g->stale = p->homelab_stale;
    draw(c);
}

// The fan, kept for draw(); and line 1's graph: its points, greyed while this feed is stale (Goal 9: it keeps its
// last points). The rest is /panel's: the plot's opacity multiplies with the box's, which draw() owns.
static void values(card_t *c, const card_val_t *v, bool stale)
{
    gpu_t *g = c->state;
    g->fstale = stale || (v && v->st == CV_STALE);
    if (g->shows_fan) {
        snprintf(g->fv, sizeof g->fv, "%s", v ? v->v : "");
        g->ff = v ? v->f : -1;
        if (g->seen) { // before the first /panel document the card shows its "—"
            draw(c);
        }
    }
    if (!g->chart) {
        return;
    }
    lv_opa_t o = stale || (v && v->st == CV_STALE) ? LV_OPA_60 : LV_OPA_COVER;
    if (lv_obj_get_style_opa(g->chart, 0) != o) {
        lv_obj_set_style_opa(g->chart, o, 0);
    }
    if (card_points(g->y[0], g->g, v && v->ng ? v->g[0] : "")) {
        lv_chart_refresh(g->chart);
    }
}

static void destroy(card_t *c) { free(c->state); }

const card_type_t card_gpu = {"gpu", create, update, destroy, values};
