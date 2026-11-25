// The tornado takeover, whatever the layout. Its own 100 ms timer takes the weather document (its own copy
// and seen counter, next to the radar card's). For the first takeover alert (the aggregator's `take`:
// Tornado / Extreme Wind Warning) not acknowledged at its level, it shows a sticky notice on the top layer
// that only its own OK dismisses, and jumps to the radar card's tab (no radar card: no jump), first ending the
// idle tour and closing the astronomical clock (ui_tour_stop). Tab swipes,
// other notes, the bell and layout changes can't touch it. Acknowledgements live in RAM: a reboot shows the
// notice again. It also owns the alert sheet (the notice's words, the radar card's badge): one chip per
// alert, the selected one's full text fetched on demand.
#include "takeover.h"

#include <ctype.h>
#include <stdio.h>
#include <string.h>
#include <time.h>

#include "esp_attr.h"
#include "net.h"
#include "page.h"
#include "ui.h"

#define DOT "\xc2\xb7"

static lv_obj_t *notice, *notice_msg;
static lv_obj_t *body; // the alert sheet's text box while that sheet exists
EXT_RAM_BSS_ATTR static weather_t wx; // the takeover's copy
EXT_RAM_BSS_ATTR static char alert_json[4096], alert_txt[4096];
EXT_RAM_BSS_ATTR static char para[2 * sizeof alert_txt + 16]; // one paragraph as recolor text (wx_markup)
static uint32_t wx_seen;
static bool active;    // a takeover alert is active, acknowledged or not
static wx_acks_t acks; // RAM only, on purpose: a reboot mid-warning shows the notice again
static char notice_key[sizeof wx.alerts[0].key];
static int notice_lvl, notice_at, want = -1; // notice_at: its alert's index; want: the shown[] index the sheet waits for
static wx_alert_t shown[WX_MAX_ALERTS]; // the sheet's chips as drawn (wx moves on while it's open)

static lv_obj_t *plain(lv_obj_t *parent)
{
    lv_obj_t *o = lv_obj_create(parent);
    lv_obj_remove_style_all(o);
    lv_obj_set_scrollable(o, false);
    lv_obj_set_clickable(o, false);
    return o;
}

uint32_t wx_color(const char *cls)
{
    return !strcmp(cls, "warning") ? WX_RED : !strcmp(cls, "watch") ? WX_AMBER : WX_ADVISORY;
}

uint32_t wx_ink(const char *cls) { return strcmp(cls, "warning") ? 0x14100a : 0xffffff; }

const char *wx_hhmm(char *buf, size_t n, long long t)
{
    time_t tt = (time_t)t;
    struct tm tm;
    localtime_r(&tt, &tm);
    strftime(buf, n, "%I:%M %p", &tm);
    return buf + (buf[0] == '0');
}

const char *wx_until(char *buf, size_t n, const wx_alert_t *a)
{
    char hm[16];
    if (a->until > 0) {
        snprintf(buf, n, "until %s", wx_hhmm(hm, sizeof hm, a->until));
    } else {
        buf[0] = '\0';
    }
    return buf;
}

static void upper(char *dst, const char *src) // dst holds strlen(src) + 1
{
    size_t i = 0;
    for (; src[i]; i++) {
        dst[i] = (char)toupper((unsigned char)src[i]);
    }
    dst[i] = '\0';
}

bool takeover_active(void) { return active; }

bool takeover_notice_open(void) { return !lv_obj_is_hidden(notice); }

void takeover_raise(void)
{
    if (notice && takeover_notice_open() && !ui_sheet_is_open()) { // (ui_init's first rebuild runs before takeover_init)
        lv_obj_move_foreground(notice); // (no redraw when it already is in front)
    }
}

int32_t takeover_notice_top(void)
{
    if (!notice || !takeover_notice_open()) {
        return -1;
    }
    lv_obj_update_layout(notice); // its height follows its text
    lv_area_t a;
    lv_obj_get_coords(notice, &a);
    return a.y1;
}

// The sticky notice for the first takeover alert not acknowledged at its level (or none). Every call while
// one is due refreshes its text and jumps to the radar card's tab (a new document, or the radar tab appearing,
// or changing name); a new alert or a raised level also brings the notice back to the front.
static void update_notice(void)
{
    int i = wx_takeover_due(&wx, &acks);
    if (i < 0) {
        lv_obj_set_hidden(notice, true);
        return;
    }
    const wx_alert_t *a = &wx.alerts[i];
    char u[24], buf[160];
    wx_notice_text(a, wx_until(u, sizeof u, a), buf, sizeof buf); // its wording follows the level
    ui_text(notice_msg, buf);
    bool fresh = lv_obj_is_hidden(notice) || strcmp(notice_key, a->key) || notice_lvl != a->lvl;
    memcpy(notice_key, a->key, sizeof notice_key);
    notice_lvl = a->lvl;
    notice_at = i;
    ui_tour_stop(); // nothing of the idle tour or the clock stays over the jump (with no radar card too)
    ui_show_tab(ui_tab_of_type("radar")); // no radar card: NULL, no jump
    if (fresh) { // over anything already up; a sheet opened later covers it until it closes
        lv_obj_set_hidden(notice, false);
        lv_obj_move_foreground(notice);
    }
}

static void on_ok(lv_event_t *e)
{
    wx_ack(&acks, notice_key, notice_lvl);
    lv_obj_set_hidden(notice, true);
    update_notice(); // the next takeover alert, if another is waiting
}

// The alert sheet: one chip per alert across the top, the selected one's full text below.
static void on_body_gone(lv_event_t *e)
{
    if (lv_event_get_target(e) == body) { // (a newer sheet may already have replaced it)
        body = NULL;
    }
}

static void on_close(lv_event_t *e) { ui_sheet_close(); }

static lv_obj_t *add_text(lv_obj_t *parent, const lv_font_t *font, uint32_t color, const char *text, int below)
{
    lv_obj_t *l = ui_label(parent, font, color);
    lv_obj_set_width(l, LV_PCT(100));
    lv_label_set_long_mode(l, LV_LABEL_LONG_MODE_WRAP);
    lv_obj_set_style_margin_bottom(l, below, 0); // px over the next
    lv_label_set_text(l, text);
    return l;
}

// The text box, as the mockup's: the headline, when, the description's paragraphs (a leading keyword in yellow)
// and the instruction in a red-edged block; or msg alone (loading, a failure). Only when a text or status arrives.
// The mockup's line-height 1.45: 16 px lines 23 px apart (a 19 px line box + 4), paragraphs 32 (+ 13); the
// headline's 29 (24 + 5).
static void fill_text(const wx_text_t *t, const char *msg)
{
    lv_obj_clean(body);
    if (msg) {
        add_text(body, font_text_16, COL_TEXT, msg, 0);
    } else {
        lv_obj_set_style_text_line_space(add_text(body, font_text_20, COL_TEXT, t->head, 2), 5, 0);
        if (t->when[0]) {
            add_text(body, font_text_14, COL_DIM, t->when, 10);
        }
        for (const char *p = t->desc; *p;) {
            const char *end = strstr(p, "\n\n");
            size_t n = end ? (size_t)(end - p) : strlen(p);
            if (n) {
                wx_markup(p, n, para, sizeof para);
                lv_obj_t *l = add_text(body, font_text_16, COL_TEXT, para, 13);
                lv_label_set_recolor(l, true);
                lv_obj_set_style_text_line_space(l, 4, 0);
            }
            p = end ? end + 2 : p + n;
        }
        if (t->instr[0]) {
            lv_obj_t *act = plain(body);
            lv_obj_set_size(act, LV_PCT(100), LV_SIZE_CONTENT);
            lv_obj_set_style_bg_color(act, lv_color_hex(0x2a1214), 0);
            lv_obj_set_style_bg_opa(act, LV_OPA_COVER, 0);
            lv_obj_set_style_border_width(act, 4, 0);
            lv_obj_set_style_border_side(act, LV_BORDER_SIDE_LEFT, 0);
            lv_obj_set_style_border_color(act, lv_color_hex(WX_RED), 0);
            lv_obj_set_style_radius(act, 6, 0);
            lv_obj_set_style_pad_ver(act, 6, 0);
            lv_obj_set_style_pad_hor(act, 10, 0);
            lv_obj_set_style_text_line_space(add_text(act, font_text_16, COL_TEXT, t->instr, 0), 4, 0);
        }
    }
    lv_obj_scroll_to_y(body, 0, LV_ANIM_OFF);
}

static void select_alert(lv_obj_t *c) // c: a chip's tap area; the chip itself is its child
{
    lv_obj_t *row = lv_obj_get_parent(c);
    for (uint32_t i = 0; i < lv_obj_get_child_count(row); i++) {
        lv_obj_t *k = lv_obj_get_child(row, (int32_t)i);
        lv_obj_set_style_border_opa(lv_obj_get_child(k, 0), k == c ? LV_OPA_COVER : LV_OPA_TRANSP, 0);
    }
    lv_obj_scroll_to_view(c, LV_ANIM_OFF); // a chip past the row's edge
    want = (int)(intptr_t)lv_obj_get_user_data(c); // an answer for any other chip is ignored
    fill_text(NULL, net_fetch_alert(shown[want].n) ? "Loading\xe2\x80\xa6" : "Couldn't load the alert");
}

static void on_chip(lv_event_t *e) { select_alert(lv_event_get_current_target(e)); }

void takeover_open_sheet(int sel) // sel: index into wx.alerts
{
    if (!wx.nalerts) {
        return;
    }
    memcpy(shown, wx.alerts, sizeof shown);
    lv_obj_t *box = ui_sheet("Weather alerts for home " DOT " from the National Weather Service");
    lv_obj_set_size(box, 720, 440);
    // Chips are ~36 px tall, so each sits in a tap area 12 px taller above and below (60 px, the touch rule).
    // The areas touch but never overlap; the row's negative margins keep the chips where they'd be without them.
    // One row: what doesn't fit scrolls sideways, so the text box keeps its height however many alerts there are.
    lv_obj_t *row = plain(box);
    lv_obj_set_size(row, LV_PCT(100), LV_SIZE_CONTENT);
    lv_obj_set_flex_flow(row, LV_FLEX_FLOW_ROW);
    lv_obj_set_scrollable(row, true);
    lv_obj_set_scroll_dir(row, LV_DIR_HOR);
    lv_obj_set_scrollbar_mode(row, LV_SCROLLBAR_MODE_OFF);
    lv_obj_set_style_pad_column(row, 8, 0);
    lv_obj_set_style_margin_ver(row, -12, 0);
    for (int i = 0; i < wx.nalerts; i++) {
        const wx_alert_t *a = &wx.alerts[i];
        lv_obj_t *t = plain(row);
        lv_obj_set_clickable(t, true);
        lv_obj_set_size(t, LV_SIZE_CONTENT, LV_SIZE_CONTENT);
        lv_obj_set_style_pad_ver(t, 12, 0);
        lv_obj_set_user_data(t, (void *)(intptr_t)i);
        lv_obj_add_event_cb(t, on_chip, LV_EVENT_CLICKED, NULL);
        lv_obj_t *c = plain(t);
        lv_obj_set_size(c, LV_SIZE_CONTENT, LV_SIZE_CONTENT);
        lv_obj_set_flex_flow(c, LV_FLEX_FLOW_ROW);
        lv_obj_set_flex_align(c, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
        lv_obj_set_style_pad_gap(c, 8, 0);
        lv_obj_set_style_pad_hor(c, 10, 0);
        lv_obj_set_style_pad_ver(c, 6, 0);
        lv_obj_set_style_radius(c, 9, 0);
        lv_obj_set_style_bg_color(c, lv_color_hex(0x1d2027), 0);
        lv_obj_set_style_bg_opa(c, LV_OPA_COVER, 0);
        lv_obj_set_style_border_width(c, 2, 0);
        lv_obj_set_style_border_color(c, lv_color_hex(COL_ACCENT), 0);
        char buf[24];
        upper(buf, a->cls);
        lv_obj_t *pill = ui_pill(c, wx_color(a->cls));
        lv_label_set_text(pill, buf);
        lv_obj_set_style_text_font(pill, font_text_12, 0);
        lv_obj_set_style_text_color(pill, lv_color_hex(wx_ink(a->cls)), 0);
        lv_label_set_text(ui_label(c, font_text_14, COL_TEXT), a->event);
        lv_label_set_text(ui_label(c, font_text_14, COL_DIM), wx_until(buf, sizeof buf, a));
    }
    body = lv_obj_create(box); // the text scrolls; fill_text() fills it
    lv_obj_set_width(body, LV_PCT(100));
    lv_obj_set_flex_grow(body, 1);
    lv_obj_set_flex_flow(body, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_style_pad_row(body, 0, 0); // the gaps are each piece's bottom margin
    lv_obj_set_style_bg_color(body, lv_color_hex(0x0f1216), 0);
    lv_obj_set_style_border_width(body, 0, 0);
    lv_obj_set_style_radius(body, 10, 0);
    lv_obj_set_style_pad_hor(body, 14, 0);
    lv_obj_set_style_pad_ver(body, 12, 0);
    lv_obj_add_event_cb(body, on_body_gone, LV_EVENT_DELETE, NULL);
    lv_obj_t *buttons = plain(box);
    lv_obj_set_size(buttons, LV_PCT(100), 56);
    lv_obj_set_flex_flow(buttons, LV_FLEX_FLOW_ROW);
    ui_button(buttons, "Close", COL_EDGE, COL_TEXT, on_close);
    select_alert(lv_obj_get_child(row, sel < wx.nalerts ? sel : 0));
}

static void on_notice_text(lv_event_t *e) { takeover_open_sheet(notice_at); }

static void tick(lv_timer_t *t)
{
    static char had[16]; // the radar card's tab at the last check, "" = none (layout.h's tab name size)
    const char *radar = ui_tab_of_type("radar");
    bool doc = net_weather_take(&wx, &wx_seen);
    if (doc) {
        active = false;
        for (int i = 0; i < wx.nalerts; i++) {
            active |= wx.alerts[i].take;
        }
    }
    // A radar tab that appears or changes name (the first layout after boot, or a rebuild mid-warning that
    // moves the radar card or renames its tab: the rebuild falls back to tab 0) gets the jump now, not at the
    // next changed document; update_notice() jumps only while a notice is unacknowledged.
    if (doc || strcmp(had, radar ? radar : "")) {
        strlcpy(had, radar ? radar : "", sizeof had);
        update_notice();
    }
    int n, status;
    if (net_alert_take(&n, &status, alert_json, sizeof alert_json) && body && want >= 0 && n == shown[want].n) {
        size_t len = strlen(alert_json);
        const char *msg = NULL;
        wx_text_t t;
        if (status != 200 || !weather_alert_text(alert_json, len, alert_txt, sizeof alert_txt, &t)) {
            msg = "Couldn't load the alert";
        } else if (!weather_alert_matches(alert_json, len, &shown[want])) {
            msg = "These alerts have changed. Close and reopen to read them.";
        }
        fill_text(&t, msg);
    }
}

void takeover_init(void)
{
    // On the top layer, so no tab, sheet, other note or layout change can dismiss it; only its OK.
    notice = lv_obj_create(lv_layer_top());
    lv_obj_set_size(notice, 640, LV_SIZE_CONTENT);
    lv_obj_align(notice, LV_ALIGN_BOTTOM_MID, 0, -96); // bottom at y 384: clear of the map credit (y ~391..410)
    lv_obj_set_style_bg_color(notice, lv_color_hex(0x2a1214), 0);
    lv_obj_set_style_border_color(notice, lv_color_hex(WX_RED), 0);
    lv_obj_set_style_border_width(notice, 2, 0);
    lv_obj_set_style_radius(notice, 12, 0);
    lv_obj_set_style_pad_hor(notice, 14, 0);
    lv_obj_set_style_pad_ver(notice, 12, 0);
    lv_obj_set_style_pad_gap(notice, 14, 0);
    lv_obj_set_flex_flow(notice, LV_FLEX_FLOW_ROW);
    lv_obj_set_flex_align(notice, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
    lv_obj_set_scrollable(notice, false);
    lv_obj_t *col = plain(notice); // tap the words: the alert sheet
    lv_obj_set_clickable(col, true);
    lv_obj_add_event_cb(col, on_notice_text, LV_EVENT_CLICKED, NULL);
    lv_obj_set_height(col, LV_SIZE_CONTENT);
    lv_obj_set_flex_grow(col, 1);
    lv_obj_set_flex_flow(col, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_style_pad_gap(col, 3, 0);
    lv_label_set_text(ui_label(col, font_text_14, 0xff8a8d), "National Weather Service " DOT " tap to read"); // mockup 13
    notice_msg = ui_label(col, font_text_17, COL_TEXT);
    lv_obj_set_width(notice_msg, LV_PCT(100));
    lv_label_set_long_mode(notice_msg, LV_LABEL_LONG_MODE_WRAP);
    lv_obj_t *ok = ui_button(notice, "OK", WX_RED, 0xffffff, on_ok);
    lv_obj_set_size(ok, 100, 60);
    lv_obj_set_flex_grow(ok, 0);
    lv_obj_set_hidden(notice, true);
    lv_timer_create(tick, 100, NULL);
}
