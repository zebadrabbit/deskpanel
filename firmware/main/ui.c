// Screen: a status bar on top, the layout's tabs in a swipeable tabview, a tab bar at the bottom.
// A 500 ms LVGL timer takes new layout, panel and cards documents from the network task (never blocking):
// a changed layout rebuilds every tab and card, a panel document goes to every card's update(), a cards
// document to every general card's values(). It also greys everything out once the panel data is older
// than STALE_MS; each general card is greyed further while the cards document is that old (or its value stale).
// The same timer runs the idle tour (below), and a tap on the status bar's time opens the astronomical clock.
#include "ui.h"

#include <stdio.h>
#include <string.h>
#include <time.h>

#include "astro_view.h"
#include "card_util.h"
#include "esp_attr.h"
#include "esp_log.h"
#include "esp_task_wdt.h"
#ifdef DESKPANEL_WDT_TEST
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#endif
#include "net.h"
#include "notify.h"
#include "startup.h"
#include "takeover.h"

#define BAR_H    48
#define TAB_H    64 // touch targets stay >= 60 px
#define STALE_MS 30000
// The 4x6 grid in the 800x368 tab: 12 px margin top, left and right, none at the bottom, 10 px gaps, so
// cells are 186.5 x 51. GRID_X(k) is grid line k's x (k = 0..4), rounded down; a card spans to the next
// line minus the gap, so every gap stays 10 px.
#define GRID_X(k) (12 + (k) * 393 / 2)
#define GRID_Y(k) (12 + (k) * 61)

static lv_obj_t *clock_lbl, *date_lbl, *stale_lbl, *wifi_lbl, *rssi_lbl, *content, *toast, *tabs, *waiting;
static lv_timer_t *toast_timer;
EXT_RAM_BSS_ATTR static panel_t data; // the UI's own copy; LVGL task only
static uint32_t seen;
// lay: what's on screen (cards' def point into it); next: what the network hands over. ~17.5 KB each.
EXT_RAM_BSS_ATTR static layout_t lay, next;
static uint32_t lay_seen;
EXT_RAM_BSS_ATTR static card_t cards[LAYOUT_MAX_TABS * LAYOUT_MAX_CARDS];
static int ncards;

extern const uint8_t ttf_start[] asm("_binary_Montserrat_Medium_ttf_start");
extern const uint8_t ttf_end[] asm("_binary_Montserrat_Medium_ttf_end");
// Weather's bold weights, subset to ASCII + ° · − (see main/fonts/OFL.txt for the licence).
extern const uint8_t xbold_start[] asm("_binary_Montserrat_ExtraBold_sub_ttf_start");
extern const uint8_t xbold_end[] asm("_binary_Montserrat_ExtraBold_sub_ttf_end");
extern const uint8_t semi_start[] asm("_binary_Montserrat_SemiBold_sub_ttf_start");
extern const uint8_t semi_end[] asm("_binary_Montserrat_SemiBold_sub_ttf_end");
const lv_font_t *font_text_12, *font_text_16, *font_text_20, *font_bold_120, *font_semi_26, *font_num_28;
const lv_font_t *font_text_14, *font_text_17, *font_text_22;
static lv_font_t num_28;
EXT_RAM_BSS_ATTR static cards_t vals; // the newest /api/v1/cards document (~30 KB)
static uint32_t vals_seen;
static bool vals_stale; // no good /cards poll for STALE_MS

static const lv_font_t *ttf(const uint8_t *start, const uint8_t *end, int px, const lv_font_t *fallback)
{
    lv_font_t *f = lv_tiny_ttf_create_data(start, end - start, px);
    f->fallback = fallback; // LV_SYMBOL_* glyphs
    return f;
}

void ui_show_tab(const char *name)
{
    for (int i = 0; name && i < lay.ntabs; i++) {
        if (!strcmp(lay.tabs[i].name, name)) {
            lv_tabview_set_active(tabs, (uint32_t)i, LV_ANIM_ON);
            return;
        }
    }
}

const char *ui_tab_of_type(const char *type)
{
    for (int i = 0; i < lay.ntabs; i++) {
        for (int j = 0; j < lay.tabs[i].ncards; j++) {
            if (!strcmp(lay.tabs[i].cards[j].type, type)) {
                return lay.tabs[i].name;
            }
        }
    }
    return NULL;
}

static lv_obj_t *sheet;

void ui_sheet_close(void)
{
    if (sheet) {
        lv_obj_delete_async(sheet); // safe from inside the sheet's own button events
        sheet = NULL;
    }
    takeover_raise(); // a pop-up or toast that came up while the sheet was open stays behind the notice
}

bool ui_sheet_is_open(void) { return sheet != NULL; }

static void on_backdrop(lv_event_t *e)
{
    if (lv_event_get_target(e) == sheet) { // the dimmed area, not the box
        ui_sheet_close();
    }
}

lv_obj_t *ui_sheet(const char *title)
{
    ui_sheet_close();
    sheet = lv_obj_create(lv_layer_top());
    lv_obj_remove_style_all(sheet);
    lv_obj_set_size(sheet, LV_PCT(100), LV_PCT(100));
    lv_obj_set_style_bg_color(sheet, lv_color_black(), 0);
    lv_obj_set_style_bg_opa(sheet, LV_OPA_60, 0);
    lv_obj_add_event_cb(sheet, on_backdrop, LV_EVENT_CLICKED, NULL);

    lv_obj_t *box = lv_obj_create(sheet);
    lv_obj_set_size(box, 640, LV_SIZE_CONTENT);
    lv_obj_set_style_max_height(box, 440, 0);
    lv_obj_align(box, LV_ALIGN_BOTTOM_MID, 0, -20);
    lv_obj_set_style_bg_color(box, lv_color_hex(COL_TILE), 0);
    lv_obj_set_style_border_color(box, lv_color_hex(COL_EDGE), 0);
    lv_obj_set_style_radius(box, 16, 0);
    lv_obj_set_style_pad_all(box, 20, 0);
    lv_obj_set_style_pad_gap(box, 12, 0);
    lv_obj_set_flex_flow(box, LV_FLEX_FLOW_COLUMN);
    lv_label_set_text(ui_label(box, font_text_16, COL_DIM), title);
    return box;
}

lv_obj_t *ui_button(lv_obj_t *parent, const char *text, uint32_t bg, uint32_t fg, lv_event_cb_t cb)
{
    lv_obj_t *b = lv_button_create(parent);
    lv_obj_set_height(b, LV_PCT(100));
    lv_obj_set_flex_grow(b, 1);
    lv_obj_set_style_bg_color(b, lv_color_hex(bg), 0);
    lv_obj_set_style_radius(b, 12, 0);
    lv_obj_add_event_cb(b, cb, LV_EVENT_CLICKED, NULL);
    lv_obj_t *l = ui_label(b, font_text_20, fg);
    lv_label_set_text(l, text);
    lv_obj_center(l);
    return b;
}

static void toast_hide(lv_timer_t *t)
{
    lv_obj_set_hidden(toast, true);
    lv_timer_pause(t);
}

void ui_toast(const char *text, uint32_t color)
{
    if (!toast) {
        toast = ui_pill(lv_layer_top(), color);
        lv_obj_set_style_text_font(toast, font_text_20, 0);
        lv_obj_set_style_pad_hor(toast, 20, 0);
        lv_obj_set_style_pad_ver(toast, 10, 0);
        toast_timer = lv_timer_create(toast_hide, 4000, NULL);
    }
    lv_label_set_text(toast, text);
    lv_obj_set_style_bg_color(toast, lv_color_hex(color), 0);
    // Above the tab bar; while the takeover notice is up (in front, over that spot), just above the notice instead.
    int32_t top = takeover_notice_top();
    int32_t h = lv_display_get_vertical_resolution(lv_display_get_default());
    lv_obj_align(toast, LV_ALIGN_BOTTOM_MID, 0, top < 0 ? -(TAB_H + 16) : top - 8 - h);
    lv_obj_set_hidden(toast, false);
    lv_timer_reset(toast_timer);
    lv_timer_resume(toast_timer);
    takeover_raise();
}

lv_obj_t *ui_label(lv_obj_t *parent, const lv_font_t *font, uint32_t color)
{
    lv_obj_t *l = lv_label_create(parent);
    lv_label_set_text(l, "");
    lv_obj_set_style_text_font(l, font, 0);
    lv_obj_set_style_text_color(l, lv_color_hex(color), 0);
    return l;
}

lv_obj_t *ui_pill(lv_obj_t *parent, uint32_t color)
{
    lv_obj_t *l = ui_label(parent, &lv_font_montserrat_16, COL_BG);
    lv_obj_set_style_bg_color(l, lv_color_hex(color), 0);
    lv_obj_set_style_bg_opa(l, LV_OPA_COVER, 0);
    lv_obj_set_style_radius(l, LV_RADIUS_CIRCLE, 0);
    lv_obj_set_style_pad_hor(l, 10, 0);
    lv_obj_set_style_pad_ver(l, 3, 0);
    return l;
}

void ui_text(lv_obj_t *label, const char *text)
{
    if (strcmp(lv_label_get_text(label), text) != 0) {
        lv_label_set_text(label, text);
    }
}

const card_type_t *card_type(const char *type)
{
    static const card_type_t *const types[] = {&card_gpu, &card_pool, &card_alerts, &card_dots, &card_board,
                                               &card_radar, &card_number, &card_gauge, &card_bars, &card_status,
                                               &card_graph};
    for (size_t i = 0; i < sizeof types / sizeof types[0]; i++) {
        if (!strcmp(types[i]->type, type)) {
            return types[i];
        }
    }
    return NULL;
}

void card_tap(const card_t *c)
{
    const char *tap = c->def->tap;
    if (!strcmp(tap, "notifications")) {
        notify_history();
    } else if (!strcmp(tap, "alert-details")) {
        nas_alerts_sheet(seen ? &data : NULL);
    } else if (!strncmp(tap, "tab:", 4)) {
        ui_show_tab(tap + 4);
    }
}

static void on_card(lv_event_t *e) { card_tap(lv_event_get_user_data(e)); }

static void update_cards(void)
{
    for (int i = 0; i < ncards; i++) {
        if (cards[i].t && cards[i].t->update) {
            cards[i].t->update(&cards[i], &data);
        }
    }
}

static void value_cards(void)
{
    for (int i = 0; i < ncards; i++) {
        if (cards[i].t && cards[i].t->values) {
            cards[i].t->values(&cards[i], cards_find(&vals, cards[i].def->id), vals_stale);
        }
    }
}

static const char *tab_name(uint32_t i) { return lay.ntabs ? lay.tabs[i < (uint32_t)lay.ntabs ? i : 0].name : "Homelab"; }

static int clamp(int v, int lo, int hi) { return v < lo ? lo : v > hi ? hi : v; }

// The idle tour (spec "Behaviour"): after the layout's idle time without a touch (5 min by default), the clock (60 s), then each
// tab (20 s) in layout order, then the clock again; the layout's tour block can change all of that. The 5 min don't count while a sheet, a notification pop-up or the takeover
// notice is up, and it doesn't start while the clock is open by hand (that stays until a tap). While it runs,
// `catcher` (invisible, full screen, the top layer's first child: under the notices, toasts and sheets, so their
// buttons still work) takes the first touch, which presses nothing underneath: it only stops the tour, and on the
// clock goes back to the tab from before the tour (A6). A pop-up, the takeover's jump and a layout rebuild stop it too.
// The timings, the clock step and the skipped tabs come from the applied layout's "tour" block (lay.tour_*; layout.c
// has the defaults and limits). tour_on false, or nothing left to show, means no tour.
static lv_obj_t *catcher;
static int tour = -1;          // the step on screen: 0 the clock, k the tab k - 1; -1 no tour
static uint32_t tour_at, home; // when that step began (lv_tick); the tab before the tour

void ui_tour_stop(void)
{
    tour = -1;
    lv_obj_set_hidden(catcher, true); // (no redraw when it already is)
    if (astro_view_is_open()) {
        astro_view_close(false);
    }
}

static void on_catch(lv_event_t *e)
{
    lv_indev_wait_release(lv_indev_active()); // the rest of this touch presses nothing either
    if (astro_view_is_open()) {
        astro_view_close(true);
        lv_tabview_set_active(tabs, home, LV_ANIM_OFF);
    }
    ui_tour_stop();
}

// The step after `from` (-1: before the first) that the layout wants shown, wrapping; -1 if none. May return `from`.
static int tour_next(int from)
{
    for (int i = 1; i <= lay.ntabs + 1; i++) {
        int s = (from + i) % (lay.ntabs + 1);
        if (s ? !((lay.tour_skip >> (s - 1)) & 1) : lay.tour_clock) {
            return s;
        }
    }
    return -1;
}

static void tour_tick(void)
{
    if (tour < 0) {
        if (ui_sheet_is_open() || notify_popup_open() || takeover_notice_open()) {
            // The 5 min don't count while one is up (answered or not): they start when it goes. Nothing else
            // reads the inactive time.
            lv_display_trigger_activity(lv_display_get_default());
        } else if (lay.ntabs && lay.tour_on && lv_display_get_inactive_time(NULL) >= lay.tour_idle_ms &&
                   !astro_view_is_open()) {
            int first = tour_next(-1);
            home = lv_tabview_get_tab_active(tabs);
            // No tour when it would change nothing: its only step is the tab already on screen (it would only swallow
            // a touch). A lone step that is another tab still starts: it brings the panel back to that tab.
            if (first >= 0 && !(first && tour_next(first) == first && (uint32_t)first - 1 == home)) {
                tour = first;
                tour_at = lv_tick_get();
                lv_obj_set_hidden(catcher, false);
                if (!tour) {
                    astro_view_open(true);
                } else {
                    lv_tabview_set_active(tabs, (uint32_t)tour - 1, LV_ANIM_OFF);
                }
            }
        }
    } else if (notify_popup_open()) { // a pop-up pauses it: the 5 min start again once it's gone
        ui_tour_stop();
    } else if (lv_tick_elaps(tour_at) >= (tour ? lay.tour_tab_ms : lay.tour_clock_ms)) {
        int next = tour_next(tour);
        tour_at = lv_tick_get();
        if (next < 0) { // (the layout can't change under a running tour, but never trap it)
            ui_tour_stop();
        } else if (next != tour) { // the only step left stays on
            int prev = tour;
            tour = next;
            if (!tour) {
                astro_view_open(true);
            } else {
                bool from_clock = astro_view_is_open();
                if (from_clock) {
                    astro_view_close(true); // shows the tabs under the still opaque clock: that tab at once, no slide
                }
                // Only a step to the next tab over slides; a longer jump or a wrap would blur across the tabs between.
                lv_tabview_set_active(tabs, (uint32_t)tour - 1, !from_clock && tour == prev + 1 ? LV_ANIM_ON : LV_ANIM_OFF);
            }
        }
    }
}

static void on_clock(lv_event_t *e) { astro_view_open(true); } // a tap on the open clock closes it (astro_view.c)

static lv_obj_t *add_tab(const char *name)
{
    lv_obj_t *tab = lv_tabview_add_tab(tabs, name);
    lv_obj_set_style_pad_all(tab, 0, 0); // cards sit at grid positions; a 4x6 card pads itself
    lv_obj_set_scrollable(tab, false);
    return tab;
}

static void place(lv_obj_t *tab, card_t *c, const layout_card_t *d)
{
    int col = clamp(d->col, 1, 4), row = clamp(d->row, 1, 6); // the aggregator checks, an old SD copy may not
    c->w = clamp(d->w, 1, 5 - col);
    c->h = clamp(d->h, 1, 7 - row);
    c->def = d;
    c->t = card_type(d->type);
    if (c->w == 4 && c->h == 6) {
        c->box = tab; // the whole tab, no grid margin
    } else {
        c->box = lv_obj_create(tab);
        lv_obj_set_pos(c->box, GRID_X(col - 1), GRID_Y(row - 1));
        lv_obj_set_size(c->box, GRID_X(col - 1 + c->w) - 10 - GRID_X(col - 1), c->h * 61 - 10);
        card_tile(c->box);
        lv_obj_set_clickable(c->box, false);
    }
    if (d->tap[0] && strcmp(d->tap, "none")) {
        lv_obj_set_clickable(c->box, true);
        lv_obj_set_style_bg_color(c->box, lv_color_hex(COL_EDGE), LV_STATE_PRESSED);
        lv_obj_add_event_cb(c->box, on_card, LV_EVENT_CLICKED, c);
    }
    if (c->t) {
        c->t->create(c);
    } else {
        char buf[40];
        snprintf(buf, sizeof buf, "unknown card '%s'", d->type);
        ui_text(card_note(c), buf);
    }
}

// Tear down every card and tab, then build them from *l (NULL: keep lay). With no tabs, one "Homelab"
// tab says what it's waiting for (tick() keeps that text current). The tab on screen stays if it's still there.
static void rebuild(const layout_t *l)
{
    char keep[sizeof lay.tabs[0].name];
    strlcpy(keep, tab_name(lv_tabview_get_tab_active(tabs)), sizeof keep);
    ui_tour_stop(); // the tour's steps are the old tabs; the 5 min start again
    lv_display_trigger_activity(lv_display_get_default());
    ui_sheet_close(); // whatever it shows may belong to a card about to go (the takeover's alert sheet too; its notice stays)
    for (int i = 0; i < ncards; i++) {
        if (cards[i].t && cards[i].t->destroy) {
            cards[i].t->destroy(&cards[i]);
        }
    }
    memset(cards, 0, sizeof cards[0] * ncards);
    ncards = 0;
    lv_obj_clean(lv_tabview_get_content(tabs));
    lv_obj_clean(lv_tabview_get_tab_bar(tabs));
    waiting = NULL;
    if (l) {
        lay = *l;
    }
    if (!lay.ntabs) {
        waiting = ui_label(add_tab("Homelab"), &lv_font_montserrat_20, COL_DIM); // never a blank screen
        lv_obj_set_style_text_align(waiting, LV_TEXT_ALIGN_CENTER, 0);
        lv_obj_center(waiting);
        return;
    }
    uint32_t active = 0;
    for (int i = 0; i < lay.ntabs; i++) {
        lv_obj_t *tab = add_tab(lay.tabs[i].name);
        for (int j = 0; j < lay.tabs[i].ncards; j++) {
            place(tab, &cards[ncards++], &lay.tabs[i].cards[j]);
        }
        active = strcmp(lay.tabs[i].name, keep) ? active : (uint32_t)i;
    }
    lv_tabview_set_active(tabs, active, LV_ANIM_OFF);
    ESP_LOGI("ui", "layout: %d tabs, %d cards", lay.ntabs, ncards);
    if (seen) {
        update_cards();
    }
    value_cards(); // the last cards document (none yet: every general card says "—")
}

static void tick(lv_timer_t *t)
{
    // Restart on a hang: this timer runs in the LVGL task every 500 ms in every state, so it feeds the task watchdog
    // (120 s, main.c). Subscribed on its first run: ui_init's direct call (t NULL) is in app_main's task.
    static bool watched;
    if (t && !watched) {
        watched = esp_task_wdt_add(NULL) == ESP_OK;
    }
    if (watched) {
        esp_task_wdt_reset();
    }
#ifdef DESKPANEL_WDT_TEST
    if (watched && lv_tick_get() > 30000) { // the deliberate-hang test (firmware/README "Self-restart"): stop feeding
        ESP_LOGW("ui", "wdt test: hanging the UI task");
        for (;;) {
            vTaskDelay(pdMS_TO_TICKS(1000));
        }
    }
#endif

    // A layout with no tabs never replaces one on screen.
    if (net_layout_take(&next, &lay_seen) && next.ntabs && !layout_equal(&next, &lay)) {
        rebuild(&next);
    }
    bool setup = waiting && net_wifi_unset(); // the flasher page's firmware, no settings.txt yet: say what to do
    if (waiting) {
        ui_text(waiting, setup ? "Wi-Fi isn't set up yet.\nPut a settings.txt on the SD card and restart.\n"
                                 "Make one at zebadrabbit.github.io/deskpanel"
                         : net_layout_status() == 404 ? "Update the aggregator to use layouts"
                                                      : "Waiting for the aggregator...");
    }
    if (net_panel_take(&data, &seen)) {
        update_cards();
        notify_update(&data);
    }
    int64_t cage = net_cards_age_ms(); // the general cards grey on their own poll's age
    bool cold = cage < 0 || cage > STALE_MS;
    if (net_cards_take(&vals, &vals_seen) || cold != vals_stale) {
        vals_stale = cold;
        value_cards();
    }

    // Stale: no good poll for STALE_MS (or none yet). Keep the last data on screen, greyed.
    int64_t age = net_panel_age_ms();
    bool old = age < 0 || age > STALE_MS;
    char buf[24];
    if (age < 0) {
        strcpy(buf, "NO DATA");
    } else if (old) {
        long long s = age / 1000; // seconds, then minutes, then hours; past 2 days just "STALE"
        if (s < 120) {
            snprintf(buf, sizeof buf, "STALE %llds", s);
        } else if (s < 2 * 3600) {
            snprintf(buf, sizeof buf, "STALE %lldm", s / 60);
        } else if (s < 2 * 86400) {
            snprintf(buf, sizeof buf, "STALE %lldh", s / 3600);
        } else {
            strcpy(buf, "STALE");
        }
    } else {
        buf[0] = '\0';
    }
    ui_text(stale_lbl, buf);
    lv_obj_set_hidden(stale_lbl, buf[0] == '\0');
    lv_opa_t opa = old && !setup ? LV_OPA_40 : LV_OPA_COVER; // nothing stale to grey under the set-up note
    if (lv_obj_get_style_opa(content, 0) != opa) {
        lv_obj_set_style_opa(content, opa, 0);
    }

    net_status_t ns = net_status();
    if (ns.time_ok) {
        time_t now = time(NULL);
        struct tm tm;
        localtime_r(&now, &tm);
        strftime(buf, sizeof buf, "%I:%M %p", &tm);
        ui_text(clock_lbl, buf[0] == '0' ? buf + 1 : buf); // 12-hour, no leading zero
        strftime(buf, sizeof buf, "%a %b %e", &tm);
        ui_text(date_lbl, buf);
    } else {
        ui_text(clock_lbl, "--:--");
        ui_text(date_lbl, "");
    }

    uint32_t c = ns.state == NET_UP ? COL_OK : ns.state == NET_CONNECTING ? COL_WARN : COL_DIM;
    lv_obj_set_style_text_color(wifi_lbl, lv_color_hex(c), 0);
    if (ns.state == NET_UP) {
        snprintf(buf, sizeof buf, "%d dBm", ns.rssi);
        ui_text(rssi_lbl, buf);
    } else {
        ui_text(rssi_lbl, ns.state == NET_CONNECTING ? "joining" : "off");
    }
    astro_view_tick(); // even while it's hidden: the sky buffer hand-off needs a take every tick
    tour_tick();
    startup_tick(lay.ntabs > 0);
}

static void on_page(lv_event_t *e)
{
    lv_obj_t *tv = lv_event_get_target(e);
    ESP_LOGI("ui", "page -> %s", tab_name(lv_tabview_get_tab_active(tv)));
}

void ui_init(lv_display_t *disp)
{
    font_text_12 = ttf(ttf_start, ttf_end, 12, &lv_font_montserrat_12); // the advisory badge's ⚠ as tall as its text
    font_text_14 = ttf(ttf_start, ttf_end, 14, &lv_font_montserrat_14);
    font_text_16 = ttf(ttf_start, ttf_end, 16, &lv_font_montserrat_16);
    font_text_17 = ttf(ttf_start, ttf_end, 17, &lv_font_montserrat_16);
    font_text_20 = ttf(ttf_start, ttf_end, 20, &lv_font_montserrat_20);
    font_text_22 = ttf(ttf_start, ttf_end, 22, &lv_font_montserrat_20);
    // 120 px glyphs are up to ~6.7 KB each, small enough to land in internal RAM: cache one temperature's 4.
    font_bold_120 = lv_tiny_ttf_create_data_ex(xbold_start, xbold_end - xbold_start, 120, LV_FONT_KERNING_NORMAL, 4);
    font_semi_26 = ttf(semi_start, semi_end, 26, &lv_font_montserrat_28);
    num_28 = lv_font_montserrat_28;
    num_28.fallback = ttf(ttf_start, ttf_end, 28, NULL);
    font_num_28 = &num_28;
    lv_display_set_theme(disp, lv_theme_default_init(disp, lv_palette_main(LV_PALETTE_BLUE),
                                                     lv_palette_main(LV_PALETTE_AMBER), true, &lv_font_montserrat_20));
    lv_obj_t *scr = lv_screen_active();
    lv_obj_set_style_bg_color(scr, lv_color_hex(COL_BG), 0);

    lv_obj_t *bar = lv_obj_create(scr);
    lv_obj_set_size(bar, LV_PCT(100), BAR_H);
    lv_obj_align(bar, LV_ALIGN_TOP_MID, 0, 0);
    lv_obj_set_style_bg_color(bar, lv_color_hex(COL_TILE), 0);
    lv_obj_set_style_radius(bar, 0, 0);
    lv_obj_set_style_border_width(bar, 0, 0);
    lv_obj_set_style_pad_hor(bar, 16, 0);
    lv_obj_set_style_pad_ver(bar, 0, 0);
    lv_obj_set_style_pad_column(bar, 12, 0);
    lv_obj_set_scrollable(bar, false);
    lv_obj_set_flex_flow(bar, LV_FLEX_FLOW_ROW);
    lv_obj_set_flex_align(bar, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);

    clock_lbl = ui_label(bar, &lv_font_montserrat_28, COL_TEXT);
    lv_obj_set_clickable(clock_lbl, true); // the astronomical clock
    lv_obj_set_ext_click_area(clock_lbl, 16);
    lv_obj_add_event_cb(clock_lbl, on_clock, LV_EVENT_CLICKED, NULL);
    date_lbl = ui_label(bar, &lv_font_montserrat_16, COL_DIM);
    lv_obj_set_flex_grow(date_lbl, 1);
    stale_lbl = ui_pill(bar, COL_WARN);
    notify_init(bar);
    wifi_lbl = ui_label(bar, &lv_font_montserrat_20, COL_DIM);
    lv_label_set_text(wifi_lbl, LV_SYMBOL_WIFI);
    rssi_lbl = ui_label(bar, &lv_font_montserrat_16, COL_DIM);

    lv_obj_t *tv = tabs = lv_tabview_create(scr);
    lv_obj_set_size(tv, LV_PCT(100), lv_display_get_vertical_resolution(disp) - BAR_H);
    lv_obj_align(tv, LV_ALIGN_BOTTOM_MID, 0, 0);
    lv_tabview_set_tab_bar_position(tv, LV_DIR_BOTTOM);
    lv_tabview_set_tab_bar_size(tv, TAB_H);
    lv_obj_set_style_bg_color(tv, lv_color_hex(COL_BG), 0);
    content = lv_tabview_get_content(tv);
    lv_obj_add_event_cb(tv, on_page, LV_EVENT_VALUE_CHANGED, NULL);
    catcher = lv_obj_create(lv_layer_top());
    lv_obj_remove_style_all(catcher);
    lv_obj_set_size(catcher, LV_PCT(100), LV_PCT(100));
    lv_obj_add_event_cb(catcher, on_catch, LV_EVENT_PRESSED, NULL);
    lv_obj_set_hidden(catcher, true);
    lv_obj_move_to_index(catcher, 0); // under everything else up there (A6)
    rebuild(NULL); // the waiting tab, until the first layout
    takeover_init(); // the tornado notice and its timer, whatever the layout holds
    astro_view_init(scr); // after the bar and the tabs, so it covers them (A7)
    startup_init(scr);    // last: the startup screen covers all of it until there's a layout

    tick(NULL);
    lv_timer_create(tick, 500, NULL);
}
