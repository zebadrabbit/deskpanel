// Radar card (4x6, one per layout): the aggregator's radar frames (800x368, base map included) loop
// full-bleed, then the map dims and the current conditions fade in over it (drawn icon, big temperature,
// humidity, condition, high/low and wind), then back to the radar: about 20 s a cycle. A 100 ms timer,
// started with the first radar card and kept after it, takes frames and weather documents whether or not
// a card is showing (a frame buffer is only reused once the UI has taken the newer set); the loop itself
// runs only while the card is on screen, from the oldest frame each time. Alerts: a badge top right (tap:
// the alert sheet), dashed outlines of warnings in view, and while takeover_active() a red frame and the
// loop only. The notice, its OK and the jump to this tab are takeover.c's, so they work without this card.
#include <stdio.h>
#include <string.h>
#include <time.h>

#include "card_util.h"
#include "ellipse.h"
#include "esp_attr.h"
#include "net.h"
#include "takeover.h"
#include "wx_icon.h"

#define FRAME_W   800
#define FRAME_H   368
#define FRAME_MS  550  // each frame
#define NEWEST_MS 1400 // hold on the newest
#define LOOPS     3
#define COND_MS   7000 // conditions on screen
#define FADE_MS   1200
#define DIM_OPA   184 // the map shows through at 28 %
#define OLD_S     900 // newest frame older than this: amber chip
#define YELLOW    0xf2c94c // severe thunderstorm outlines
#define DASH      8 // outline dash and gap, px
#define GAP       5
#define PULSE_MS  1100

#define MINUS "\xe2\x88\x92" // U+2212, a typographic minus
#define DOT   "\xc2\xb7"
#define DEG   "\xc2\xb0"

// dsc[i].data is swapped in place when a new frame set arrives. lv_image_cache_drop() isn't public in
// LVGL 9.6, so this relies on the image cache staying off (sdkconfig and the simulator: both 0); a cached
// entry would keep pointing at the old buffer.
#if LV_CACHE_DEF_SIZE
#error "card_radar.c swaps image data in place: drop the cached entry after each swap"
#endif

// What outlives a card: the timer, the newest frames and document, and the image descriptors.
static lv_timer_t *timer;
EXT_RAM_BSS_ATTR static weather_t wx; // the card's copy (takeover.c keeps its own)
static uint32_t wx_seen, fr_seen;
static net_frames_t fr;
static lv_image_dsc_t dsc[WX_FRAMES]; // dsc[i] shows fr.px[i]

// The card on screen: its objects go with its tab (and their animations with them); owner NULL: none.
static const card_t *owner;
static lv_obj_t *view, *img, *marker, *dim, *chip, *dots, *dot[WX_FRAMES], *overlay, *icon, *text;
static lv_obj_t *temp, *hum, *cond, *hilo, *attr, *msg;
static lv_obj_t *outlines, *outline[WX_MAX_POLYS], *badge, *badge_lbl, *more, *frame;
static int cur, loops, faded; // frame on screen, loops done, fade level (0 radar .. 255 conditions)
static bool conditions, was_visible, take; // take: takeover_active() as last shown (red frame, radar only)
static uint32_t due; // lv_tick_get() when the loop moves on

static lv_obj_t *plain(lv_obj_t *parent)
{
    lv_obj_t *o = lv_obj_create(parent);
    lv_obj_remove_style_all(o);
    lv_obj_set_scrollable(o, false);
    lv_obj_set_clickable(o, false);
    return o;
}

static lv_obj_t *circle(lv_obj_t *parent, int d, uint32_t color, lv_opa_t opa)
{
    lv_obj_t *o = plain(parent);
    lv_obj_set_size(o, d, d);
    lv_obj_set_style_radius(o, LV_RADIUS_CIRCLE, 0);
    lv_obj_set_style_bg_color(o, lv_color_hex(color), 0);
    lv_obj_set_style_bg_opa(o, opa, 0);
    return o;
}

static void show_radar(bool on) // the map and everything that only makes sense on it
{
    lv_obj_t *radar[] = {img, marker, outlines, chip, dots, attr};
    for (size_t i = 0; i < sizeof radar / sizeof radar[0]; i++) {
        lv_obj_set_hidden(radar[i], !on);
    }
}

static void show_frame(int i)
{
    cur = i;
    lv_image_set_src(img, &dsc[i]);
    for (int k = 0; k < WX_FRAMES; k++) {
        lv_obj_set_hidden(dot[k], k >= fr.n);
        lv_obj_set_style_bg_opa(dot[k], k == i ? LV_OPA_COVER : 64, 0);
    }
    // Minutes before the newest frame; once radar is stuck (amber), honest minutes before now instead.
    long long ref = fr.t[fr.n - 1], now = time(NULL);
    bool old = net_status().time_ok && now - ref > OLD_S;
    long long min = ((old ? now : ref) - fr.t[i]) / 60;
    char hm[16], buf[48];
    const char *at = wx_hhmm(hm, sizeof hm, fr.t[i]);
    if (min > 0) {
        snprintf(buf, sizeof buf, MINUS "%lld min " DOT " %s", min, at);
    } else {
        snprintf(buf, sizeof buf, "now " DOT " %s", at);
    }
    ui_text(chip, buf);
    lv_obj_set_style_text_color(chip, lv_color_hex(old ? WX_AMBER : COL_TEXT), 0);
}

// Only while fading does anything blend over the whole map; at 0 the dim layer and overlay are hidden.
static void fade_cb(void *var, int32_t v)
{
    faded = v;
    lv_obj_set_hidden(dim, v == 0);
    lv_obj_set_hidden(overlay, v == 0);
    lv_obj_set_style_bg_opa(dim, (lv_opa_t)(v * DIM_OPA / 255), 0);
    lv_obj_set_style_opa_layered(icon, (lv_opa_t)v, 0); // as one layer: no sun showing through the cloud
    lv_obj_set_style_opa(text, (lv_opa_t)v, 0);
    lv_obj_t *radar_only[] = {marker, outlines, chip, dots}; // the marker sits right where the temperature goes
    for (size_t i = 0; i < sizeof radar_only / sizeof radar_only[0]; i++) {
        lv_obj_set_style_opa(radar_only[i], (lv_opa_t)(255 - v), 0);
    }
}

static void fade(int32_t to)
{
    lv_anim_t a;
    lv_anim_init(&a);
    lv_anim_set_var(&a, dim);
    lv_anim_set_exec_cb(&a, fade_cb);
    lv_anim_set_values(&a, faded, to);
    lv_anim_set_duration(&a, FADE_MS);
    lv_anim_set_path_cb(&a, lv_anim_path_ease_in_out);
    lv_anim_start(&a);
}

static void restart(void) // radar, oldest frame, no fade
{
    lv_anim_delete(dim, fade_cb);
    fade_cb(dim, 0);
    conditions = false;
    loops = 0;
    if (fr.n) {
        show_frame(0);
    }
    due = lv_tick_get() + FRAME_MS;
}

static void show_now(void)
{
    const wx_now_t *n = &wx.now;
    char buf[64];
    snprintf(buf, sizeof buf, "%s%d" DEG, n->t < 0 ? MINUS : "", n->t < 0 ? -n->t : n->t);
    ui_text(temp, buf);
    snprintf(buf, sizeof buf, "Humidity %d%%", n->rh);
    ui_text(hum, buf);
    ui_text(cond, n->text);
    snprintf(buf, sizeof buf, "H %d" DEG " " DOT " L %d" DEG "%s%s", n->hi, n->lo, n->wind[0] ? " " DOT " wind " : "",
             n->wind);
    ui_text(hilo, buf);
    wx_icon_set(icon, n->code, n->day);
}

// --- alerts ---

// The red badge's glow and the home outline's opacity breathe; both are small objects, so each step
// redraws only a small area. v = 0 is the resting look.
static void glow_cb(void *o, int32_t v)
{
    lv_obj_set_style_shadow_spread(o, v * 7 / 255, 0);
    lv_obj_set_style_shadow_opa(o, (lv_opa_t)(v * 0x55 / 255), 0);
}

static void breathe_cb(void *o, int32_t v)
{
    if (faded == 255 && v) { // under the conditions the outlines are invisible: don't redraw the map for nothing
        return;
    }
    lv_obj_set_style_opa(o, (lv_opa_t)(255 - v / 2), 0);
}

static void pulse(lv_obj_t *o, lv_anim_exec_xcb_t cb, bool on)
{
    if (!on) {
        lv_anim_delete(o, cb);
        cb(o, 0);
        return;
    }
    if (lv_anim_get(o, cb)) {
        return; // already pulsing: keep the rhythm
    }
    lv_anim_t a;
    lv_anim_init(&a);
    lv_anim_set_var(&a, o);
    lv_anim_set_exec_cb(&a, cb);
    lv_anim_set_values(&a, 0, 255);
    lv_anim_set_duration(&a, PULSE_MS / 2);
    lv_anim_set_reverse_duration(&a, PULSE_MS / 2);
    lv_anim_set_repeat_count(&a, LV_ANIM_REPEAT_INFINITE);
    lv_anim_set_path_cb(&a, lv_anim_path_ease_in_out);
    lv_anim_start(&a);
}

// lv_line dashes only level and upright segments (LVGL 9.6's software renderer) and warning outlines are
// mostly slanted, so each outline draws its own: closed, DASH on and GAP off along the whole perimeter.
static void draw_outline(lv_event_t *e)
{
    lv_obj_t *o = lv_event_get_current_target(e);
    const wx_poly_t *p = &wx.polys[(intptr_t)lv_event_get_user_data(e)];
    lv_area_t v;
    lv_obj_get_coords(outlines, &v); // the points are frame pixels; outlines covers the frame
    lv_draw_line_dsc_t d;
    lv_draw_line_dsc_init(&d);
    lv_obj_init_draw_line_dsc(o, LV_PART_MAIN, &d);
    int ph = 0; // where in the dash pattern the walk is
    for (int k = 0; k < p->npts; k++) {
        int x = p->pts[k][0], y = p->pts[k][1];
        int dx = p->pts[(k + 1) % p->npts][0] - x, dy = p->pts[(k + 1) % p->npts][1] - y;
        int len = lv_sqrt32((uint32_t)(dx * dx + dy * dy));
        for (int s = 0; s < len;) {
            int end = LV_MIN(len, s + (ph < DASH ? DASH - ph : DASH + GAP - ph));
            if (ph < DASH) {
                d.p1.x = v.x1 + x + dx * s / len;
                d.p1.y = v.y1 + y + dy * s / len;
                d.p2.x = v.x1 + x + dx * end / len;
                d.p2.y = v.y1 + y + dy * end / len;
                lv_draw_line(lv_event_get_layer(e), &d);
            }
            ph = (ph + end - s) % (DASH + GAP);
            s = end;
        }
    }
}

static void show_alerts(void) // badge and outlines: from the newest document
{
    char up[64], u[24], buf[96];
    lv_obj_set_hidden(badge, !wx.nalerts);
    if (wx.nalerts) { // the most severe, "+N" for the rest
        const wx_alert_t *a = &wx.alerts[0];
        bool warn = !strcmp(a->cls, "warning"), small = wx_color(a->cls) == WX_ADVISORY;
        wx_badge_text(a, wx_until(u, sizeof u, a), up, sizeof up);
        snprintf(buf, sizeof buf, LV_SYMBOL_WARNING " %s", up);
        ui_text(badge_lbl, buf);
        lv_obj_set_style_text_font(badge_lbl, warn ? font_text_16 : small ? font_text_12 : font_text_14, 0); // mockup
        lv_obj_set_style_text_color(badge_lbl, lv_color_hex(wx_ink(a->cls)), 0);
        lv_obj_set_style_bg_color(badge, lv_color_hex(wx_color(a->cls)), 0);
        lv_obj_set_style_pad_hor(badge, warn ? 14 : small ? 10 : 12, 0);
        lv_obj_set_style_pad_ver(badge, warn ? 8 : small ? 4 : 6, 0);
        snprintf(buf, sizeof buf, "+%d", wx.nalerts - 1);
        ui_text(more, buf);
        lv_obj_set_hidden(more, wx.nalerts < 2);
    }
    pulse(badge, glow_cb, wx.nalerts && !strcmp(wx.alerts[0].cls, "warning"));

    for (int i = 0; i < WX_MAX_POLYS; i++) { // each outline object spans just its polygon
        const wx_poly_t *p = &wx.polys[i];
        lv_obj_t *o = outline[i];
        bool on = i < wx.npolys && p->npts >= 2;
        lv_obj_set_hidden(o, !on);
        pulse(o, breathe_cb, on && p->home);
        if (!on) {
            continue;
        }
        int x1 = FRAME_W, y1 = FRAME_H, x2 = 0, y2 = 0;
        for (int k = 0; k < p->npts; k++) {
            x1 = LV_MIN(x1, p->pts[k][0]);
            y1 = LV_MIN(y1, p->pts[k][1]);
            x2 = LV_MAX(x2, p->pts[k][0]);
            y2 = LV_MAX(y2, p->pts[k][1]);
        }
        lv_obj_set_pos(o, x1 - 4, y1 - 4); // room for half a 4 px line
        lv_obj_set_size(o, x2 - x1 + 9, y2 - y1 + 9);
        lv_obj_set_style_line_color(o, lv_color_hex(p->kind == 't' ? WX_RED : YELLOW), 0);
        lv_obj_set_style_line_width(o, p->home ? 4 : 2, 0);
        lv_obj_invalidate(o); // same box, new points
    }
}

static void on_badge(lv_event_t *e) { takeover_open_sheet(0); }

static void show_take(void) // the red frame and loop-only follow takeover_active()
{
    if (takeover_active() != take) {
        take = !take;
        lv_obj_set_hidden(frame, !take);
        if (take) {
            due = lv_tick_get(); // off the conditions now
        }
    }
}

static void tick(lv_timer_t *t)
{
    // Card or not, keep taking: the net task refills a buffer only once the UI has moved past it.
    bool frames = net_frames_take(&fr, &fr_seen), doc = net_weather_take(&wx, &wx_seen);
    if (frames) { // onto the new buffers right now: the old ones may be refilled
        for (int i = 0; i < fr.n; i++) {
            dsc[i].data = (const uint8_t *)fr.px[i]; // (no image cache to drop: see the #error above)
        }
    }
    if (!owner) {
        return; // create() draws what's newest
    }
    if (frames) {
        show_radar(fr.n);
        if (fr.n) {
            show_frame(cur < fr.n ? cur : fr.n - 1);
        }
    }
    if (doc) {
        if (wx.has_now) {
            show_now();
        }
        show_alerts();
    }
    show_take();

    bool unset = net_weather_status() == 404; // the only status that says more than the STALE pill
    const char *m = unset                    ? "Weather isn't set up on the aggregator\n(WEATHER_LAT / WEATHER_LON)"
                    : !fr.n && !wx.has_now ? "Waiting for weather"
                                           : "";
    ui_text(msg, m);
    lv_obj_set_hidden(msg, !m[0]);
    lv_obj_set_hidden(view, unset);

    bool visible = lv_obj_is_visible(view);
    if (visible != was_visible) {
        was_visible = visible;
        restart();
    }
    if (!visible || (int32_t)(lv_tick_get() - due) < 0) {
        return;
    }
    if (!fr.n) { // no radar yet: conditions alone, if there are any
        if (wx.has_now && !conditions) {
            conditions = true;
            fade(255);
        }
        due = lv_tick_get() + (conditions ? COND_MS : FRAME_MS);
        return;
    }
    if (conditions) { // back to the radar, from the oldest frame
        conditions = false;
        fade(0);
        show_frame(0);
        due = lv_tick_get() + FADE_MS;
        return;
    }
    int next = cur + 1;
    if (next >= fr.n) {
        next = 0;
        loops++;
    }
    if (loops >= LOOPS) {
        loops = 0;
        if (wx.has_now && !take) { // dim the newest frame and show the conditions over it
            conditions = true;
            fade(255);
            due = lv_tick_get() + FADE_MS + COND_MS;
            return;
        }
    }
    show_frame(next);
    if ((int32_t)(lv_tick_get() - due) > FRAME_MS) { // fell behind (a stall): carry on from now, don't race
        due = lv_tick_get();
    }
    due += next == fr.n - 1 ? NEWEST_MS : FRAME_MS;
}

static void create(card_t *c)
{
    if (owner) { // one radar card: a second has nothing it could show differently
        ui_text(card_note(c), "already on another tab");
        return;
    }
    owner = c;
    cur = loops = 0;
    conditions = was_visible = take = false;
    lv_obj_t *parent = c->box;
    lv_obj_set_style_pad_all(parent, 0, 0);
    lv_obj_set_scrollable(parent, false);
    view = plain(parent);
    lv_obj_set_size(view, FRAME_W, FRAME_H);

    for (int i = 0; i < WX_FRAMES; i++) {
        dsc[i].header.magic = LV_IMAGE_HEADER_MAGIC;
        dsc[i].header.cf = LV_COLOR_FORMAT_RGB565;
        dsc[i].header.w = FRAME_W;
        dsc[i].header.h = FRAME_H;
        dsc[i].header.stride = FRAME_W * 2;
        dsc[i].data_size = FRAME_W * FRAME_H * 2;
    }
    img = lv_image_create(view);

    // Home, at the frame centre: a white dot in a soft ring. Part of the map, so under the dim layer.
    marker = plain(view);
    lv_obj_set_size(marker, LV_PCT(100), LV_PCT(100));
    lv_obj_t *ring = circle(marker, 28, 0xffffff, LV_OPA_TRANSP);
    lv_obj_set_width(ring, ellipse_w(28)); // round on the glass
    lv_obj_set_style_border_width(ring, 2, 0);
    lv_obj_set_style_border_color(ring, lv_color_white(), 0);
    lv_obj_set_style_border_opa(ring, LV_OPA_60, 0);
    lv_obj_center(ring);
    lv_obj_center(circle(marker, 14, 0xffffff, LV_OPA_COVER));
    lv_obj_t *home = ui_label(marker, font_text_14, 0xe8eaed);
    lv_label_set_text(home, "home");
    lv_obj_align_to(home, ring, LV_ALIGN_OUT_RIGHT_MID, 2, -10);

    dim = plain(view);
    lv_obj_set_size(dim, LV_PCT(100), LV_PCT(100));
    lv_obj_set_style_bg_color(dim, lv_color_hex(COL_BG), 0);

    outlines = plain(view); // warnings in view: over the dim layer, under the chip; they fade with the radar
    lv_obj_set_size(outlines, LV_PCT(100), LV_PCT(100));
    for (int i = 0; i < WX_MAX_POLYS; i++) {
        outline[i] = plain(outlines);
        lv_obj_add_event_cb(outline[i], draw_outline, LV_EVENT_DRAW_MAIN, (void *)(intptr_t)i);
        lv_obj_set_hidden(outline[i], true);
    }

    chip = ui_label(view, font_text_14, COL_TEXT);
    lv_obj_set_style_bg_color(chip, lv_color_black(), 0);
    lv_obj_set_style_bg_opa(chip, 187, 0);
    lv_obj_set_style_radius(chip, 8, 0);
    lv_obj_set_style_pad_hor(chip, 10, 0);
    lv_obj_set_style_pad_ver(chip, 5, 0);
    lv_obj_set_pos(chip, 14, 12);

    dots = plain(view);
    lv_obj_set_size(dots, LV_SIZE_CONTENT, LV_SIZE_CONTENT);
    lv_obj_set_flex_flow(dots, LV_FLEX_FLOW_ROW);
    lv_obj_set_style_pad_column(dots, 6, 0);
    lv_obj_align(dots, LV_ALIGN_BOTTOM_LEFT, 14, -12);
    for (int k = 0; k < WX_FRAMES; k++) {
        dot[k] = circle(dots, 9, 0xffffff, 64);
    }

    overlay = plain(view);
    lv_obj_set_size(overlay, LV_PCT(100), LV_PCT(100));
    lv_obj_set_flex_flow(overlay, LV_FLEX_FLOW_ROW);
    lv_obj_set_flex_align(overlay, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
    lv_obj_set_style_pad_column(overlay, 34, 0);
    icon = wx_icon_create(overlay, 170);
    text = plain(overlay);
    lv_obj_set_size(text, LV_SIZE_CONTENT, LV_SIZE_CONTENT);
    lv_obj_set_flex_flow(text, LV_FLEX_FLOW_COLUMN);
    temp = ui_label(text, font_bold_120, COL_TEXT);
    lv_obj_set_style_text_letter_space(temp, -4, 0);
    hum = ui_label(text, font_semi_26, COL_TEXT);
    lv_obj_set_style_margin_top(hum, -16, 0); // the 120 px line box is mostly air below the digits
    cond = ui_label(text, font_text_22, 0xc9cfd8);
    lv_obj_set_style_margin_top(cond, 4, 0);
    hilo = ui_label(text, font_text_17, COL_DIM);
    lv_obj_set_style_margin_top(hilo, 10, 0);

    badge = plain(view); // colours and text per alert in show_alerts()
    lv_obj_set_clickable(badge, true);
    lv_obj_set_ext_click_area(badge, 12);
    lv_obj_add_event_cb(badge, on_badge, LV_EVENT_CLICKED, NULL);
    lv_obj_set_size(badge, LV_SIZE_CONTENT, LV_SIZE_CONTENT);
    lv_obj_set_flex_flow(badge, LV_FLEX_FLOW_ROW);
    lv_obj_set_flex_align(badge, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
    lv_obj_set_style_pad_column(badge, 8, 0);
    lv_obj_set_style_radius(badge, 8, 0);
    lv_obj_set_style_bg_opa(badge, LV_OPA_COVER, 0);
    lv_obj_set_style_shadow_width(badge, 2, 0);
    lv_obj_set_style_shadow_color(badge, lv_color_hex(WX_RED), 0);
    lv_obj_align(badge, LV_ALIGN_TOP_RIGHT, -14, 12);
    badge_lbl = ui_label(badge, font_text_16, COL_BG);
    more = ui_label(badge, font_text_12, 0xffffff);
    lv_obj_set_style_bg_color(more, lv_color_black(), 0);
    lv_obj_set_style_bg_opa(more, 102, 0);
    lv_obj_set_style_radius(more, 6, 0);
    lv_obj_set_style_pad_hor(more, 6, 0);
    lv_obj_set_style_pad_ver(more, 1, 0);
    pulse(badge, glow_cb, false);
    lv_obj_set_hidden(badge, true);

    attr = ui_label(view, font_text_12, COL_DIM); // the base map's licence asks for visible credit
    lv_label_set_text(attr, "\xc2\xa9 OpenStreetMap contributors");
    lv_obj_set_style_bg_color(attr, lv_color_black(), 0);
    lv_obj_set_style_bg_opa(attr, LV_OPA_50, 0);
    lv_obj_set_style_radius(attr, 4, 0);
    lv_obj_set_style_pad_hor(attr, 6, 0);
    lv_obj_set_style_pad_ver(attr, 2, 0);
    lv_obj_align(attr, LV_ALIGN_BOTTOM_RIGHT, -8, -6);

    frame = plain(view); // while a takeover alert is active
    lv_obj_set_size(frame, LV_PCT(100), LV_PCT(100));
    lv_obj_set_style_border_width(frame, 4, 0);
    lv_obj_set_style_border_color(frame, lv_color_hex(WX_RED), 0);
    lv_obj_set_hidden(frame, true);

    msg = ui_label(parent, font_text_20, COL_DIM); // never a blank tab
    lv_obj_set_width(msg, 640);
    lv_label_set_long_mode(msg, LV_LABEL_LONG_MODE_WRAP);
    lv_obj_set_style_text_align(msg, LV_TEXT_ALIGN_CENTER, 0);
    lv_obj_center(msg);

    // What the timer took before this card (none at boot; after a layout change, the newest of everything).
    show_radar(fr.n);
    fade_cb(dim, 0);
    if (fr.n) {
        show_frame(0);
    }
    if (wx.has_now) {
        show_now();
    }
    show_alerts();
    show_take();
    if (!timer) {
        timer = lv_timer_create(tick, 100, NULL);
    }
}

static void destroy(card_t *c)
{
    if (owner == c) {
        owner = NULL; // ui.c deletes the objects next; LVGL deletes their animations with them
    }
}

const card_type_t card_radar = {"radar", create, NULL, destroy, NULL};
