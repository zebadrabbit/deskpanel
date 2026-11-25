// The startup screen: see startup.h. Measurements are the brand kit's startup spec's (the kit isn't in the repo), in
// panel pixels. The logo is the kit's 312x244 logo block as raw little-endian RGB565 (startup_logo.rgb565;
// firmware/README.md has the command), already narrowed for the panel: never scaled.
// Left out of the design: the background rings (three screen-wide arcs in every frame of a fade) and the build
// line (the build has no git to ask).
#include "startup.h"

#include <math.h>

#include "net.h"
#include "page.h"

#define NIGHT 0x000810 // Night #070B14 as 16 bits show it: what the logo image's own background is, in the sim's 24 bits too
#define BRASS 0xd8b56a
#define MOON  0xefe3bd
#define MUTED 0x3a4760
#define LIGHT_MS    300 // main.c lights the panel 150 ms after the first frame: the fade starts once it's lit
#define FADE_IN_MS  600
#define FADE_OUT_MS 250 // twice: the logo into the dark, then the dark into the tabs
#define WAVE_PTS    18 // every 2 px across the wave's 35

extern const uint8_t logo_start[] asm("_binary_startup_logo_rgb565_start");
static const lv_image_dsc_t logo = {
    .header = {.magic = LV_IMAGE_HEADER_MAGIC, .cf = LV_COLOR_FORMAT_RGB565, .w = 312, .h = 244, .stride = 624},
    .data_size = 312 * 244 * 2,
    .data = logo_start,
};

bool startup_off;
static lv_obj_t *root, *art, *wave, *status, *more, *row, *dot[3], *name[3];
static lv_timer_t *wave_timer;
static lv_point_precise_t pts[WAVE_PTS];
static uint32_t born;
static bool leaving;
static int shown[3] = {-1, -1, -1}, warned = -1;

// One sine cycle across the chart box, scrolling right to left: a cycle every 12 steps of 100 ms. The line is the
// whole box inside, so each step redraws all of it (LVGL culls a line by its ends: see ellipse.h).
static void wave_step(lv_timer_t *t)
{
    static int step;
    for (int i = 0; i < WAVE_PTS; i++) {
        pts[i].x = 6 + 2 * i;
        pts[i].y = 24 + (lv_value_precise_t)lroundf(11 * sinf(2 * (float)M_PI * (2 * i / 35.f + step / 12.f)));
    }
    step = (step + 1) % 12;
    lv_line_set_points(wave, pts, WAVE_PTS);
}

// The wave fades as one picture (a layer of its small box): faded segment by segment, its round joints show through.
static void set_opa(void *o, int32_t v)
{
    lv_obj_set_style_opa(o, (lv_opa_t)v, 0);
    lv_obj_set_style_opa_layered(wave, (lv_opa_t)v, 0);
}

static void fade(lv_obj_t *o, int from, int to, int ms, int delay, lv_anim_completed_cb_t done)
{
    lv_anim_t a;
    lv_anim_init(&a);
    lv_anim_set_var(&a, o);
    lv_anim_set_exec_cb(&a, set_opa);
    lv_anim_set_values(&a, from, to);
    lv_anim_set_duration(&a, ms);
    lv_anim_set_delay(&a, delay);
    lv_anim_set_path_cb(&a, lv_anim_path_ease_in_out);
    lv_anim_set_completed_cb(&a, done);
    lv_anim_start(&a);
}

static void gone(lv_anim_t *a)
{
    LV_UNUSED(a);
    lv_timer_delete(wave_timer);
    lv_obj_delete(root);
    root = NULL;
}

static void set_root_opa(void *o, int32_t v) { lv_obj_set_style_opa(o, (lv_opa_t)v, 0); }

// The way out is the way in, backwards: the logo into the dark, then the dark into the tabs. (Fading it all at once
// would blend the logo's opaque rectangle over the already faded background: a darker box over the tabs.)
static void dark(lv_anim_t *a)
{
    LV_UNUSED(a);
    lv_anim_t b;
    lv_anim_init(&b);
    lv_anim_set_var(&b, root);
    lv_anim_set_exec_cb(&b, set_root_opa);
    lv_anim_set_values(&b, LV_OPA_COVER, LV_OPA_TRANSP);
    lv_anim_set_duration(&b, FADE_OUT_MS);
    lv_anim_set_completed_cb(&b, gone);
    lv_anim_start(&b);
}

void startup_init(lv_obj_t *screen)
{
    if (startup_off) {
        return;
    }
    born = lv_tick_get();
    root = lv_obj_create(screen); // opaque from the first frame: the light comes on over the dark, never over the tabs
    lv_obj_remove_style_all(root);
    lv_obj_set_size(root, LV_PCT(100), LV_PCT(100));
    lv_obj_set_style_bg_color(root, lv_color_hex(NIGHT), 0);
    lv_obj_set_style_bg_opa(root, LV_OPA_COVER, 0);
    lv_obj_set_scrollable(root, false); // (still clickable: it takes every touch meant for what's under it)

    art = lv_obj_create(root); // everything that fades in
    lv_obj_remove_style_all(art);
    lv_obj_set_size(art, LV_PCT(100), LV_PCT(100));
    lv_obj_set_clickable(art, false);
    lv_obj_set_scrollable(art, false);

    lv_obj_t *img = lv_image_create(art);
    lv_image_set_src(img, &logo);
    lv_obj_set_pos(img, 244, 70);

    wave = lv_line_create(root); // the chart box's clear inside: 46 x 48 centred on (363, 148). Not in art: see set_opa()
    lv_obj_set_pos(wave, 340, 124);
    lv_obj_set_size(wave, 46, 48);
    lv_obj_set_style_line_width(wave, 7, 0);
    lv_obj_set_style_line_rounded(wave, true, 0);
    lv_obj_set_style_line_color(wave, lv_color_hex(MOON), 0);
    wave_step(NULL);
    wave_timer = lv_timer_create(wave_step, 100, NULL);

    status = ui_label(art, font_text_20, COL_TEXT);
    lv_obj_align(status, LV_ALIGN_TOP_MID, 0, 326);
    more = ui_label(art, font_text_17, COL_DIM); // how to set Wi-Fi up, when it isn't
    lv_obj_set_style_text_align(more, LV_TEXT_ALIGN_CENTER, 0);
    lv_obj_set_style_text_line_space(more, 6, 0);
    lv_label_set_text(more, "Put a settings.txt on the SD card and restart.\nMake one at zebadrabbit.github.io/deskpanel");
    lv_obj_align(more, LV_ALIGN_TOP_MID, 0, 360);
    lv_obj_set_hidden(more, true);

    row = lv_obj_create(art);
    lv_obj_remove_style_all(row);
    lv_obj_set_size(row, LV_SIZE_CONTENT, LV_SIZE_CONTENT);
    lv_obj_set_flex_flow(row, LV_FLEX_FLOW_ROW);
    lv_obj_set_flex_align(row, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
    lv_obj_set_clickable(row, false);
    static const char *const names[3] = {"WI-FI", "AGGREGATOR", "LAYOUT"};
    for (int i = 0; i < 3; i++) {
        dot[i] = lv_obj_create(row);
        lv_obj_remove_style_all(dot[i]);
        lv_obj_set_size(dot[i], 10, 11); // round on the glass
        lv_obj_set_style_radius(dot[i], LV_RADIUS_CIRCLE, 0);
        lv_obj_set_style_bg_opa(dot[i], LV_OPA_COVER, 0);
        lv_obj_set_style_margin_left(dot[i], i ? 34 : 0, 0);
        name[i] = ui_label(row, font_text_12, MUTED);
        lv_obj_set_style_text_letter_space(name[i], 1, 0);
        lv_obj_set_style_margin_left(name[i], 9, 0);
        lv_label_set_text(name[i], names[i]);
    }
    lv_obj_align(row, LV_ALIGN_TOP_MID, 0, 382);

    startup_tick(false);
    set_opa(art, LV_OPA_TRANSP);
    fade(art, LV_OPA_TRANSP, LV_OPA_COVER, FADE_IN_MS, LIGHT_MS, NULL);
}

void startup_tick(bool have_layout)
{
    if (!root || leaving) {
        return;
    }
    if (have_layout && lv_tick_elaps(born) >= LIGHT_MS + FADE_IN_MS + STARTUP_HOLD_MS) {
        leaving = true;
        fade(art, LV_OPA_COVER, LV_OPA_TRANSP, FADE_OUT_MS, 0, dark);
        return;
    }
    // What net.c can tell: Wi-Fi's state, a first good panel poll or any answer about the layout, a layout taken.
    net_state_t wifi = net_status().state;
    int ls = net_layout_status();
    bool setup = net_wifi_unset(), agg = net_panel_age_ms() >= 0 || ls > 0;
    int warn = setup || (ls == 404 && !have_layout);
    ui_text(status, setup                    ? "Wi-Fi isn't set up yet."
                    : warn                   ? "Update the aggregator to use layouts"
                    : have_layout || agg     ? "Loading your layout…"
                    : wifi == NET_UP         ? "Waiting for the aggregator…"
                    : wifi == NET_CONNECTING ? "Joining Wi-Fi…"
                                             : "Starting…");
    if (warn != warned) {
        warned = warn;
        lv_obj_set_style_text_color(status, lv_color_hex(warn ? COL_WARN : COL_TEXT), 0);
        lv_obj_set_hidden(more, !setup);
        lv_obj_set_hidden(row, setup);
    }
    // Per dot: 0 not yet (muted), 1 working on it (brass), 2 done (green), 3 it said no (warn).
    int st[3] = {wifi == NET_UP ? 2 : wifi == NET_CONNECTING, agg ? 2 : wifi == NET_UP,
                 have_layout ? 2 : ls == 404 ? 3 : agg};
    static const uint32_t col[4] = {MUTED, BRASS, COL_OK, COL_WARN};
    for (int i = 0; i < 3; i++) {
        if (st[i] != shown[i]) {
            shown[i] = st[i];
            lv_obj_set_style_bg_color(dot[i], lv_color_hex(col[st[i]]), 0);
            lv_obj_set_style_text_color(name[i], lv_color_hex(st[i] ? COL_DIM : MUTED), 0);
        }
    }
}
