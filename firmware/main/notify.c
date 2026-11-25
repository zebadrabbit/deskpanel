// Notifications. The newest unread note pops up under the status bar with an OK button; left alone
// it goes away after NOTICE_MS and the bell keeps the unread count. The bell opens the history.
// OK and opening the history report "read up to id" to the aggregator, which owns read state, so a
// reboot never re-announces old notes. The document carries the newest 5; the history here keeps 20.
// Notes with act "wx" (tornado takeover) never pop up here: they only go to the bell and the history
// (takeover.c has the notice).
#include "notify.h"

#include <stdio.h>
#include <string.h>
#include <time.h>

#include "esp_attr.h"
#include "net.h"
#include "page.h"
#include "takeover.h"

#define NOTICE_MS   30000
#define HISTORY_MAX 20

static lv_obj_t *bell, *bell_lbl, *notice, *notice_src, *notice_msg;
static lv_timer_t *notice_timer;
static int notice_id, acked, toasted; // acked: highest id read here; toasted: highest id popped up
EXT_RAM_BSS_ATTR static panel_note_t history[HISTORY_MAX]; // newest first
static int nhistory;

static uint32_t lvl_color(const char *lvl)
{
    return !strcmp(lvl, "alert") ? COL_BAD : !strcmp(lvl, "warn") ? COL_WARN : COL_ACCENT;
}

static void refresh_bell(void)
{
    int n = 0;
    for (int i = 0; i < nhistory; i++) {
        n += !history[i].read && history[i].id > acked;
    }
    char buf[16];
    if (n) {
        snprintf(buf, sizeof buf, LV_SYMBOL_BELL " %d", n);
    } else {
        strcpy(buf, LV_SYMBOL_BELL);
    }
    ui_text(bell_lbl, buf);
    lv_obj_set_style_text_color(bell_lbl, lv_color_hex(n ? COL_WARN : COL_DIM), 0);
}

static void mark_read(int upto)
{
    if (upto > acked) {
        acked = upto;
    }
    net_mark_read(acked);
    refresh_bell();
}

static void hide_notice(void)
{
    lv_obj_set_hidden(notice, true);
    lv_timer_pause(notice_timer);
}

bool notify_popup_open(void) { return !lv_obj_is_hidden(notice); }

static void on_notice_timeout(lv_timer_t *t) { hide_notice(); } // the bell still shows it unread
static void on_ok(lv_event_t *e)
{
    hide_notice();
    mark_read(notice_id);
}

static void show_notice(const panel_note_t *n)
{
    notice_id = n->id;
    ui_text(notice_src, n->src[0] ? n->src : "Notice");
    ui_text(notice_msg, n->msg);
    lv_obj_set_style_border_color(notice, lv_color_hex(lvl_color(n->lvl)), 0);
    lv_obj_set_hidden(notice, false);
    lv_obj_move_foreground(notice);
    takeover_raise(); // the takeover notice stays in front
    lv_timer_reset(notice_timer);
    lv_timer_resume(notice_timer);
}

static void fmt_when(char *buf, size_t n, long long t)
{
    time_t when = (time_t)t, now = time(NULL);
    struct tm a, b;
    localtime_r(&when, &a);
    localtime_r(&now, &b);
    char hm[12], day[12];
    strftime(hm, sizeof hm, "%I:%M %p", &a);
    const char *h = hm[0] == '0' ? hm + 1 : hm; // 12-hour, no leading zero
    if (a.tm_yday == b.tm_yday && a.tm_year == b.tm_year) {
        snprintf(buf, n, "%s", h);
    } else {
        strftime(day, sizeof day, "%b %e", &a);
        snprintf(buf, n, "%s, %s", day, h);
    }
}

static void on_close(lv_event_t *e) { ui_sheet_close(); }

static void on_bell(lv_event_t *e) { notify_history(); }

void notify_history(void)
{
    lv_obj_t *box = ui_sheet("Notifications");
    if (!nhistory) {
        lv_label_set_text(ui_label(box, font_text_20, COL_DIM), "Nothing yet.");
    }
    for (int i = 0; i < nhistory; i++) {
        const panel_note_t *n = &history[i];
        bool unseen = !n->read && n->id > acked;
        lv_obj_t *row = lv_obj_create(box);
        lv_obj_remove_style_all(row);
        lv_obj_set_size(row, LV_PCT(100), LV_SIZE_CONTENT);
        lv_obj_set_flex_flow(row, LV_FLEX_FLOW_COLUMN);
        lv_obj_set_style_pad_gap(row, 2, 0);
        char when[32];
        fmt_when(when, sizeof when, n->t);
        lv_obj_t *head = ui_label(row, font_text_16, unseen ? lvl_color(n->lvl) : COL_DIM);
        lv_label_set_text_fmt(head, "%s  \xC2\xB7  %s", n->src[0] ? n->src : "Notice", when); // middle dot
        lv_obj_t *msg = ui_label(row, font_text_20, unseen ? COL_TEXT : COL_DIM);
        lv_obj_set_width(msg, LV_PCT(100));
        lv_label_set_long_mode(msg, LV_LABEL_LONG_MODE_WRAP);
        lv_label_set_text(msg, n->msg);
    }
    lv_obj_t *buttons = lv_obj_create(box);
    lv_obj_remove_style_all(buttons);
    lv_obj_set_size(buttons, LV_PCT(100), 64);
    lv_obj_set_flex_flow(buttons, LV_FLEX_FLOW_ROW);
    ui_button(buttons, "Close", COL_EDGE, COL_TEXT, on_close);
    hide_notice();
    if (nhistory) {
        mark_read(history[0].id); // opening the history reads everything in it
    }
}

void notify_init(lv_obj_t *bar)
{
    bell = lv_obj_create(bar); // a wide hit area around a small icon
    lv_obj_remove_style_all(bell);
    lv_obj_set_size(bell, 64, LV_PCT(100));
    lv_obj_set_style_bg_color(bell, lv_color_hex(COL_EDGE), LV_STATE_PRESSED);
    lv_obj_set_style_bg_opa(bell, LV_OPA_COVER, LV_STATE_PRESSED);
    lv_obj_add_event_cb(bell, on_bell, LV_EVENT_CLICKED, NULL);
    bell_lbl = ui_label(bell, &lv_font_montserrat_20, COL_DIM);
    lv_obj_center(bell_lbl);
    refresh_bell();

    notice = lv_obj_create(lv_layer_top());
    lv_obj_set_size(notice, 600, LV_SIZE_CONTENT);
    lv_obj_align(notice, LV_ALIGN_TOP_MID, 0, 56);
    lv_obj_set_style_bg_color(notice, lv_color_hex(COL_TILE), 0);
    lv_obj_set_style_border_width(notice, 3, 0);
    lv_obj_set_style_radius(notice, 14, 0);
    lv_obj_set_style_pad_all(notice, 14, 0);
    lv_obj_set_style_pad_gap(notice, 14, 0);
    lv_obj_set_style_shadow_width(notice, 24, 0);
    lv_obj_set_style_shadow_color(notice, lv_color_black(), 0);
    lv_obj_set_flex_flow(notice, LV_FLEX_FLOW_ROW);
    lv_obj_set_flex_align(notice, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
    lv_obj_set_scrollable(notice, false);
    lv_obj_t *col = lv_obj_create(notice);
    lv_obj_remove_style_all(col);
    lv_obj_set_height(col, LV_SIZE_CONTENT);
    lv_obj_set_flex_grow(col, 1);
    lv_obj_set_flex_flow(col, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_style_pad_gap(col, 4, 0);
    notice_src = ui_label(col, font_text_16, COL_DIM);
    notice_msg = ui_label(col, font_text_20, COL_TEXT);
    lv_obj_set_width(notice_msg, LV_PCT(100));
    lv_label_set_long_mode(notice_msg, LV_LABEL_LONG_MODE_WRAP);
    lv_obj_t *ok = ui_button(notice, "OK", COL_OK, COL_BG, on_ok);
    lv_obj_set_size(ok, 100, 64);
    lv_obj_set_flex_grow(ok, 0);
    lv_obj_set_hidden(notice, true);
    notice_timer = lv_timer_create(on_notice_timeout, NOTICE_MS, NULL);
    lv_timer_pause(notice_timer);
}

void notify_update(const panel_t *p)
{
    // Merge the document's notes into the history (newest first, by id); refresh their read flags.
    for (int i = 0; i < p->nnotes; i++) {
        const panel_note_t *n = &p->notes[i];
        int at = 0;
        while (at < nhistory && history[at].id > n->id) {
            at++;
        }
        if (at < nhistory && history[at].id == n->id) {
            history[at].read = n->read;
            continue;
        }
        if (at >= HISTORY_MAX) {
            continue; // older than everything we keep
        }
        int keep = nhistory < HISTORY_MAX ? nhistory : HISTORY_MAX - 1;
        memmove(&history[at + 1], &history[at], sizeof history[0] * (keep - at));
        history[at] = *n;
        nhistory = keep + 1;
    }

    if (nhistory && !history[0].read && history[0].id > acked && history[0].id > toasted) {
        toasted = history[0].id;
        if (strcmp(history[0].act, "wx")) { // takeover notes: takeover.c shows its own sticky notice
            show_notice(&history[0]);
        }
    }
    // Something read here still unread there (a lost report)? Report again.
    for (int i = 0; i < p->nnotes; i++) {
        if (!p->notes[i].read && p->notes[i].id <= acked) {
            net_mark_read(acked);
            break;
        }
    }
    refresh_bell();
}
