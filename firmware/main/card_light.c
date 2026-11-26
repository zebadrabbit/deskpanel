// Light card (values() from /api/v1/cards: v "off" / "on" / "NN%", v2 the light's colour rrggbb, f brightness 0..1,
// th what it can do: 1 dims, 2 colour temperature, 4 colour), at 1x1, 2x1, 1x2 and 2x2:
//   1 row    a round dot, the title, the state on the right
//   2 rows   the same head, the state large under it (with no state: the reason, smaller, wrapped)
// The dot is the light's colour when on (brass without one), muted when off. While on, the tile's outline takes that
// colour too, and the tile fills with it (faintly, behind the text) from the bottom up to the brightness: a switch
// that is on is full. A tap turns it off or on, by what the tile shows; a long press opens
// the sheet (brightness slider, colour swatches, On / Off) when the light can do more than switch. Every action
// shows at once (optimistic) and is kept until /cards agrees, the request fails (the old state comes back, with a
// toast) or 10 s pass. What's pending lives here, keyed by card id: a layout change rebuilds every card.
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "card_util.h"
#include "ellipse.h"
#include "esp_timer.h"
#include "net.h"

#define BRASS       0xd8b56a // "on"
#define FILL_OPA    71       // the brightness fill, 28 %: the text over it stays clear, on pure blue too
#define FILL_MIN    3        // px: a light at 1 % still shows it is on
#define PENDING_MS  10000    // drop an expected state /cards never confirmed
#define MAX_PENDING 8        // the request queue's length

// The aggregator's fixed colours (lights.go): the name its route takes, the capability it needs, the swatch.
static const struct { const char *name; int needs; uint32_t rgb; } colors[] = {
    {"soft", 2, 0xffa957}, {"neutral", 2, 0xffd1a3}, {"cool", 2, 0xe6f0ff}, {"red", 4, 0xff0000},
    {"amber", 4, 0xff9500}, {"green", 4, 0x00ff00}, {"blue", 4, 0x0000ff}, {"magenta", 4, 0xff00ff}};
#define NCOLORS ((int)(sizeof colors / sizeof colors[0]))

typedef struct light {
    struct light *next; // every light card on screen
    card_t *c;
    lv_obj_t *dot, *val;
    const lv_font_t *small; // two rows: the font of a reason in the state's place; NULL on one row
    lv_obj_t *level, *fill; // the brightness fill: level is the window (bottom up), fill the tile-sized colour in it
    int room;               // px: the tile's height inside its border, the fill's at 100 %
    bool none, stale;       // none: no readable state (v is the reason, taps do nothing)
    char v[16], v2[16];     // the aggregator's
    float f, last_f;        // last_f: the brightness it last had while on (0: never seen on)
    int caps;
} light_t;

typedef struct {
    char id[24]; // "" = free
    int64_t since_us;
    char kind;                    // 'o' on/off, 'b' brightness, 'c' colour
    char v[16], v2[16], was[16];  // what the tile shows meanwhile; was: the aggregator's v ('b') or v2 ('c') before
    float f;
} pending_t;

static light_t *lights;
static pending_t pending[MAX_PENDING];
static lv_timer_t *results;
static char sheet_id[24];                            // the sheet's card; "" = no light sheet
static lv_obj_t *sheet_box, *sheet_slider, *sheet_pct;

static const char *title(const light_t *l) { return l->c->def->title[0] ? l->c->def->title : l->c->def->id; }

static pending_t *find_pending(const char *id)
{
    for (int i = 0; i < MAX_PENDING; i++) {
        if (pending[i].id[0] && !strcmp(pending[i].id, id)) {
            return &pending[i];
        }
    }
    return NULL;
}

static light_t *find_light(const char *id)
{
    light_t *l = lights;
    while (l && strcmp(l->c->def->id, id)) {
        l = l->next;
    }
    return l;
}

static void set_pct(int pct)
{
    char buf[16];
    snprintf(buf, sizeof buf, "%d%%", pct);
    ui_text(sheet_pct, buf);
}

// The tile (and the sheet, if it's this card's) from what's pending, else from the aggregator's values.
static void paint(light_t *l)
{
    const pending_t *p = l->none ? NULL : find_pending(l->c->def->id);
    const char *v = p ? p->v : l->v, *v2 = p ? p->v2 : l->v2;
    float f = p ? p->f : l->f;
    bool on = !l->none && strcmp(v, "off");
    uint32_t color = !on ? COL_EDGE : v2[0] ? (uint32_t)strtoul(v2, NULL, 16) : BRASS;
    int pct = on && f > 0 ? (int)(f * 100 + 0.5f) : 0;
    int h = !on ? 0 : f > 0 && f < 1 ? LV_MAX((int)(l->room * f + 0.5f), FILL_MIN) : l->room; // on, no brightness: full
    const lv_font_t *font = l->none ? l->small : font_num_28;
    if (l->small && lv_obj_get_style_text_font(l->val, 0) != font) {
        lv_obj_set_style_text_font(l->val, font, 0);
    }
    ui_text(l->val, v);
    card_color(l->val, LV_STYLE_TEXT_COLOR, LV_PART_MAIN, on ? COL_TEXT : COL_DIM);
    card_color(l->dot, LV_STYLE_BG_COLOR, LV_PART_MAIN, color);
    card_edge(l->c->box, on && !l->stale ? color : COL_EDGE);
    lv_obj_set_hidden(l->level, !on);
    if (on) { // only what changed: a set redraws
        card_color(l->fill, LV_STYLE_BG_COLOR, LV_PART_MAIN, color);
        if (lv_obj_get_style_height(l->level, 0) != h) {
            lv_obj_set_height(l->level, h);
        }
    }
    // The sheet follows, except under a finger; an off light keeps the slider where it was.
    if (sheet_slider && !strcmp(sheet_id, l->c->def->id) && pct && !lv_obj_has_state(sheet_slider, LV_STATE_PRESSED) &&
        lv_slider_get_value(sheet_slider) != pct) {
        lv_slider_set_value(sheet_slider, pct, LV_ANIM_OFF);
        set_pct(pct);
    }
}

// Does the aggregator's state (in *l) show the pending action's result?
static bool agrees(const pending_t *p, const light_t *l)
{
    bool on = strcmp(l->v, "off");
    switch (p->kind) {
    case 'b': // the percentage asked for, or (rounded differently on the way) any other than before
        return !strcmp(l->v, p->v) || strcmp(l->v, p->was);
    case 'c': // the light reports its own rgb, not the swatch's: any change of colour
        return on && strcmp(l->v2, p->was);
    default:
        return on == (strcmp(p->v, "off") != 0);
    }
}

// Queue `action` ("toggle", "on", "off", "bri/N", "color/<name>") and show what it will do at once.
static void act(light_t *l, const char *action)
{
    const char *id = l->c->def->id;
    pending_t *slot = find_pending(id), next = {.kind = 'o', .since_us = esp_timer_get_time()};
    const char *v = slot ? slot->v : l->v; // what the tile shows now
    bool on = strcmp(v, "off");
    strlcpy(next.id, id, sizeof next.id);
    strlcpy(next.v, v, sizeof next.v);
    strlcpy(next.v2, slot ? slot->v2 : l->v2, sizeof next.v2);
    next.f = slot ? slot->f : l->f;
    if (!strncmp(action, "bri/", 4)) {
        next.kind = 'b';
        snprintf(next.v, sizeof next.v, "%s%%", action + 4);
        next.f = (float)atoi(action + 4) / 100;
        strlcpy(next.was, l->v, sizeof next.was);
    } else if (!strncmp(action, "color/", 6)) {
        next.kind = 'c';
        strlcpy(next.was, l->v2, sizeof next.was);
        for (int i = 0; i < NCOLORS; i++) {
            if (!strcmp(action + 6, colors[i].name)) {
                snprintf(next.v2, sizeof next.v2, "%06x", (unsigned)colors[i].rgb);
            }
        }
        if (!on) { // a colour turns it on, at a brightness nobody knows yet
            strcpy(next.v, "on");
            next.f = l->last_f > 0 ? l->last_f : 1;
        }
    } else if ((!strcmp(action, "toggle") ? !on : !strcmp(action, "on")) != on) {
        strcpy(next.v, on ? "off" : "on");
        next.f = on ? 0 : l->last_f > 0 ? l->last_f : 1; // back on: as bright as it last was, as far as we know
    }
    for (int i = 0; !slot && i < MAX_PENDING; i++) {
        slot = pending[i].id[0] ? NULL : &pending[i];
    }
    if (!slot || !net_light(id, action)) {
        ui_toast("Busy - try again in a moment", COL_BAD);
        return;
    }
    *slot = next;
    paint(l);
}

const char *card_act_error(int status)
{
    static char buf[40];
    snprintf(buf, sizeof buf, "Couldn't do that (HTTP %d)", status);
    return status < 0 ? "Couldn't reach the aggregator" : status == 502 ? "Home Assistant didn't answer" :
           status == 409 ? "That light can't do that" : status == 503 ? "Home Assistant isn't set up" : buf;
}

// Every 250 ms: the finished requests (the profile cards' too: one reader for the one queue), and the expected
// states that have waited PENDING_MS. A failure or a timeout puts the aggregator's state back on the tile.
static void poll_results(lv_timer_t *t)
{
    net_act_t r;
    while (net_act_result(&r)) {
        if (!strcmp(r.action, "apply") || !strcmp(r.action, "save")) {
            card_profile_result(r.card_id, r.action, r.status);
        } else if (r.status < 200 || r.status >= 300) {
            pending_t *p = find_pending(r.card_id);
            light_t *l = find_light(r.card_id);
            if (p) {
                p->id[0] = '\0';
            }
            if (l) {
                paint(l);
            }
            ui_toast(card_act_error(r.status), COL_BAD);
        } // done: the expected state stays until /cards shows it
    }
    for (int i = 0; i < MAX_PENDING; i++) {
        if (pending[i].id[0] && esp_timer_get_time() - pending[i].since_us > PENDING_MS * 1000LL) {
            light_t *l = find_light(pending[i].id);
            pending[i].id[0] = '\0';
            if (l) {
                paint(l);
            }
        }
    }
}

void card_acts_start(void)
{
    if (!results) { // never deleted: a result may come back after its card has gone
        results = lv_timer_create(poll_results, 250, NULL);
    }
}

// --- the sheet ---
static void on_sheet_gone(lv_event_t *e)
{
    if (lv_event_get_target(e) == sheet_box) { // (an older sheet's box is deleted after the next one is up)
        sheet_id[0] = '\0';
        sheet_box = sheet_slider = sheet_pct = NULL;
    }
}

static void sheet_act(const char *action)
{
    light_t *l = find_light(sheet_id);
    if (l && !l->none) {
        act(l, action);
    }
}

static void on_slide(lv_event_t *e) { set_pct((int)lv_slider_get_value(sheet_slider)); }

static void on_slid(lv_event_t *e) // the finger left the slider: one request, never one per step of a drag
{
    char action[16];
    snprintf(action, sizeof action, "bri/%d", (int)lv_slider_get_value(sheet_slider));
    sheet_act(action);
}

static void on_swatch(lv_event_t *e)
{
    char action[16];
    snprintf(action, sizeof action, "color/%s", colors[(intptr_t)lv_event_get_user_data(e)].name);
    sheet_act(action);
}

static void on_on(lv_event_t *e) { sheet_act("on"); }
static void on_off(lv_event_t *e) { sheet_act("off"); }
static void on_close(lv_event_t *e) { ui_sheet_close(); }

static void open_sheet(light_t *l)
{
    lv_obj_t *box = ui_sheet(title(l));
    sheet_box = box;
    sheet_slider = sheet_pct = NULL;
    strlcpy(sheet_id, l->c->def->id, sizeof sheet_id);
    lv_obj_add_event_cb(box, on_sheet_gone, LV_EVENT_DELETE, NULL);
    if (l->caps & 1) {
        lv_obj_t *row = card_plain(box, LV_FLEX_FLOW_ROW, 24);
        lv_obj_set_size(row, LV_PCT(100), 56);
        lv_obj_set_style_pad_left(row, 18, 0); // the knob reaches past the slider's ends
        lv_obj_set_flex_align(row, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
        lv_obj_t *s = sheet_slider = lv_slider_create(row);
        lv_obj_set_height(s, 12);
        lv_obj_set_flex_grow(s, 1);
        lv_obj_set_ext_click_area(s, 22); // a 56 px touch target
        lv_slider_set_range(s, 1, 100);
        lv_slider_set_value(s, 100, LV_ANIM_OFF);
        lv_obj_set_style_bg_color(s, lv_color_hex(COL_EDGE), LV_PART_MAIN);
        lv_obj_set_style_bg_opa(s, LV_OPA_COVER, LV_PART_MAIN);
        lv_obj_set_style_bg_color(s, lv_color_hex(BRASS), LV_PART_INDICATOR);
        lv_obj_set_style_bg_color(s, lv_color_hex(COL_TEXT), LV_PART_KNOB);
        lv_obj_set_style_pad_all(s, 10, LV_PART_KNOB); // a 32 px knob
        lv_obj_add_event_cb(s, on_slide, LV_EVENT_VALUE_CHANGED, NULL);
        lv_obj_add_event_cb(s, on_slid, LV_EVENT_RELEASED, NULL);
        sheet_pct = ui_label(row, font_text_20, COL_TEXT);
        lv_obj_set_width(sheet_pct, 60);
        lv_obj_set_style_text_align(sheet_pct, LV_TEXT_ALIGN_RIGHT, 0);
        set_pct(100);
    }
    if (l->caps & 6) {
        lv_obj_t *row = card_plain(box, LV_FLEX_FLOW_ROW, 20);
        lv_obj_set_size(row, LV_PCT(100), 56);
        lv_obj_set_flex_align(row, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
        for (int i = 0; i < NCOLORS; i++) {
            if (!(l->caps & colors[i].needs)) {
                continue;
            }
            lv_obj_t *sw = card_plain(row, LV_FLEX_FLOW_ROW, 0);
            lv_obj_set_size(sw, ellipse_w(48), 48); // round on the glass
            lv_obj_set_style_radius(sw, LV_RADIUS_CIRCLE, 0);
            lv_obj_set_style_bg_color(sw, lv_color_hex(colors[i].rgb), 0);
            lv_obj_set_style_bg_opa(sw, LV_OPA_COVER, 0);
            lv_obj_set_style_bg_opa(sw, LV_OPA_60, LV_STATE_PRESSED);
            lv_obj_set_ext_click_area(sw, 8);
            lv_obj_set_clickable(sw, true);
            lv_obj_add_event_cb(sw, on_swatch, LV_EVENT_CLICKED, (void *)(intptr_t)i);
        }
    }
    lv_obj_t *row = card_plain(box, LV_FLEX_FLOW_ROW, 16);
    lv_obj_set_size(row, LV_PCT(100), 64);
    ui_button(row, "Off", COL_EDGE, COL_TEXT, on_off);
    ui_button(row, "On", BRASS, COL_BG, on_on);
    ui_button(row, "Close", COL_EDGE, COL_TEXT, on_close);
    paint(l); // the slider to where the light is
}

// --- the tile ---
static void on_tap(lv_event_t *e)
{
    light_t *l = lv_event_get_user_data(e);
    if (!l->none) { // "on" or "off" by what the tile shows, never "toggle": the light may have changed since the
        const pending_t *p = find_pending(l->c->def->id); // last poll, and a toggle would then do the opposite
        act(l, strcmp(p ? p->v : l->v, "off") ? "off" : "on");
    }
}

static void on_hold(lv_event_t *e)
{
    light_t *l = lv_event_get_user_data(e);
    if (!l->none && l->caps) { // a switch has nothing more to offer
        open_sheet(l);
    }
}

static void create(card_t *c)
{
    light_t *l = c->state = calloc(1, sizeof *l);
    l->c = c;
    l->none = true;
    l->next = lights;
    lights = l;
    card_acts_start();
    lv_obj_set_clickable(c->box, true);
    lv_obj_set_style_bg_color(c->box, lv_color_hex(COL_EDGE), LV_STATE_PRESSED);
    // Never LV_EVENT_CLICKED: LVGL sends that after a long press too.
    lv_obj_add_event_cb(c->box, on_tap, LV_EVENT_SHORT_CLICKED, l);
    lv_obj_add_event_cb(c->box, on_hold, LV_EVENT_LONG_PRESSED, l);

    // The fill, first so it lies behind the text: a window `level` on the tile's bottom edge, as tall as the light
    // is bright, onto `fill`, which is the whole inside of the tile with its corners. So the fill's foot follows the
    // tile's rounded corners and its top is straight, without clipping the tile itself (that would cost a layer).
    int32_t edge = lv_obj_get_style_border_width(c->box, 0), pad = lv_obj_get_style_pad_bottom(c->box, 0);
    int32_t w = lv_obj_get_style_width(c->box, 0) - 2 * edge;
    l->room = lv_obj_get_style_height(c->box, 0) - 2 * edge;
    l->level = card_plain(c->box, LV_FLEX_FLOW_ROW, 0);
    lv_obj_set_floating(l->level, true); // not one of the tile's rows
    lv_obj_set_size(l->level, w, 0);
    lv_obj_align(l->level, LV_ALIGN_BOTTOM_MID, 0, pad);
    lv_obj_set_hidden(l->level, true);
    l->fill = card_plain(l->level, LV_FLEX_FLOW_ROW, 0);
    lv_obj_set_floating(l->fill, true);
    lv_obj_set_size(l->fill, w, l->room);
    lv_obj_align(l->fill, LV_ALIGN_BOTTOM_MID, 0, 0);
    lv_obj_set_style_radius(l->fill, lv_obj_get_style_radius(c->box, 0) - edge, 0);
    lv_obj_set_style_bg_opa(l->fill, FILL_OPA, 0);

    bool big = c->w >= 2 || c->h >= 2; // all but 1x1; a title too long for its line ends in "..." (card_line)
    const lv_font_t *font = big ? font_text_20 : font_text_16;
    int d = big ? 18 : 14;
    lv_obj_t *h = card_head(c->box);
    l->dot = card_plain(h, LV_FLEX_FLOW_ROW, 0);
    lv_obj_set_size(l->dot, ellipse_w(d), d); // round on the glass
    lv_obj_set_style_radius(l->dot, LV_RADIUS_CIRCLE, 0);
    lv_obj_set_style_bg_opa(l->dot, LV_OPA_COVER, 0);
    lv_obj_t *t = card_line(h, font, COL_TEXT);
    lv_obj_set_flex_grow(t, 1);
    ui_text(t, title(l));
    if (c->h == 1) {
        l->val = card_line(h, font, COL_DIM);
        lv_obj_set_width(l->val, LV_SIZE_CONTENT);
        lv_obj_set_style_max_width(l->val, LV_PCT(60), 0); // a long reason leaves the title some room
        lv_obj_set_flex_align(c->box, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_START);
        return;
    }
    l->small = c->w >= 2 ? font_text_20 : font_text_16; // two rows: the state large under the head; a reason (no state) smaller, wrapped, whole
    l->val = ui_label(c->box, font_num_28, COL_DIM);
    lv_obj_set_width(l->val, LV_PCT(100));
}

static void values(card_t *c, const card_val_t *v, bool stale)
{
    light_t *l = c->state;
    l->stale = stale || (v && v->st == CV_STALE);
    card_opa(c, l->stale, false); // greyed, with the plain edge (paint()): what it shows is the last we heard
    l->none = !v || v->st == CV_NONE || !v->v[0];
    strlcpy(l->v, l->none ? card_value(v, true) : v->v, sizeof l->v); // none: the aggregator's reason, or "—"
    strlcpy(l->v2, l->none ? "" : v->v2, sizeof l->v2);
    l->f = l->none ? 0 : v->f;
    l->caps = l->none || v->th < 0 ? 0 : v->th;
    l->last_f = !l->none && strcmp(l->v, "off") && l->f > 0 ? l->f : l->last_f;
    pending_t *p = find_pending(c->def->id);
    if (p && (l->none || agrees(p, l))) {
        p->id[0] = '\0';
    }
    paint(l);
}

static void destroy(card_t *c)
{
    for (light_t **at = &lights; *at; at = &(*at)->next) {
        if (*at == c->state) {
            *at = (*at)->next;
            break;
        }
    }
    free(c->state);
}

const card_type_t card_light = {"light", create, NULL, destroy, values};
