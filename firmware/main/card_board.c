// Board card (4x6, one per layout): one tile per Planka board the deskpanel user is a member of, flipped
// vertically (the horizontal swipe belongs to the tabs) and auto-advancing while nobody touches it. Each
// tile lists the board's most recently updated cards. Tap an open card -> confirm sheet -> Done: the card
// shows done at once (optimistic), the network task POSTs it, and a failure puts it back. Pending dones,
// the board on show and the last touch outlive the card, so a layout change keeps them.
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <time.h>

#include "card_util.h"
#include "esp_attr.h"
#include "esp_timer.h"
#include "net.h"

#define ADVANCE_MS  10000 // flip to the next board this often...
#define IDLE_MS     30000 // ...but not within this long of a touch
#define PENDING_MS  60000 // drop an optimistic "done" the data never confirmed
#define CARD_H      76    // touch target
#define MAX_PENDING 8

typedef struct {
    char id[24];
    int64_t since_us;
} pending_t;

static const card_t *owner; // the one board card, or NULL; the objects and timers below are its
static lv_obj_t *root, *tv, *dots, *message;
static lv_timer_t *results, *advancer;
EXT_RAM_BSS_ATTR static panel_board_t shown[PANEL_MAX_BOARDS]; // on screen, pending dones applied
EXT_RAM_BSS_ATTR static panel_board_t next[PANEL_MAX_BOARDS];
static int nshown = -1, current;
static const panel_t *latest; // ui.c's copy; valid until the next update()
static pending_t pending[MAX_PENDING];
static int64_t touched_us;
static char sheet_card[24];

static int64_t now_us(void) { return esp_timer_get_time(); }

static pending_t *find_pending(const char *id)
{
    for (int i = 0; i < MAX_PENDING; i++) {
        if (pending[i].id[0] && !strcmp(pending[i].id, id)) {
            return &pending[i];
        }
    }
    return NULL;
}

static void update_dots(void)
{
    for (uint32_t i = 0; i < lv_obj_get_child_count(dots); i++) {
        lv_obj_set_style_bg_color(lv_obj_get_child(dots, i), lv_color_hex((int)i == current ? COL_ACCENT : COL_EDGE), 0);
    }
}

static void on_tile(lv_event_t *e)
{
    current = (int)lv_obj_get_index(lv_tileview_get_tile_active(tv));
    update_dots();
}

static void on_scroll(lv_event_t *e)
{
    if (lv_indev_active()) { // a finger, not the auto-advance animation
        touched_us = now_us();
    }
}

static void render(void);

static void on_done(lv_event_t *e)
{
    pending_t *slot = NULL;
    for (int i = 0; !slot && i < MAX_PENDING; i++) {
        if (!pending[i].id[0]) {
            slot = &pending[i];
        }
    }
    if (slot && net_mark_done(sheet_card)) {
        strlcpy(slot->id, sheet_card, sizeof slot->id);
        slot->since_us = now_us();
    } else {
        ui_toast("Busy - try again in a moment", COL_BAD);
    }
    ui_sheet_close();
    render();
}

static void on_cancel(lv_event_t *e) { ui_sheet_close(); }

static void open_sheet(const panel_board_t *b, const panel_card_t *c)
{
    strlcpy(sheet_card, c->id, sizeof sheet_card);
    lv_obj_t *box = ui_sheet("Mark this card done?");
    lv_obj_t *l = ui_label(box, font_text_20, COL_TEXT);
    lv_obj_set_width(l, LV_PCT(100));
    lv_label_set_text(l, c->name);
    l = ui_label(box, font_text_16, COL_DIM);
    lv_label_set_text_fmt(l, "%s  \xC2\xB7  %s", b->name, c->list); // middle dot

    lv_obj_t *row = card_plain(box, LV_FLEX_FLOW_ROW, 16);
    lv_obj_set_size(row, LV_PCT(100), 64);
    ui_button(row, "Cancel", COL_EDGE, COL_TEXT, on_cancel);
    ui_button(row, LV_SYMBOL_OK "  Done", COL_OK, COL_BG, on_done);
}

static void on_card(lv_event_t *e)
{
    touched_us = now_us();
    int i = (int)(intptr_t)lv_event_get_user_data(e);
    open_sheet(&shown[i / PANEL_MAX_CARDS], &shown[i / PANEL_MAX_CARDS].cards[i % PANEL_MAX_CARDS]);
}

static lv_obj_t *chip(lv_obj_t *parent, const panel_label_t *lb)
{
    unsigned c = lb->color;
    int luma = (int)(((c >> 16) & 0xff) * 299 + ((c >> 8) & 0xff) * 587 + (c & 0xff) * 114) / 1000;
    lv_obj_t *p = ui_pill(parent, c);
    lv_obj_set_style_text_font(p, font_text_16, 0);
    lv_obj_set_style_text_color(p, lv_color_hex(luma > 150 ? COL_BG : 0xffffff), 0);
    lv_label_set_text(p, lb->name);
    return p;
}

static void card_row(lv_obj_t *tile, const panel_board_t *b, int bi, int ci)
{
    const panel_card_t *c = &b->cards[ci];
    bool tappable = !c->done && b->can_done;
    lv_obj_t *row = lv_obj_create(tile);
    lv_obj_set_size(row, LV_PCT(100), CARD_H);
    lv_obj_set_style_bg_color(row, lv_color_hex(COL_TILE), 0);
    lv_obj_set_style_bg_color(row, lv_color_hex(COL_EDGE), LV_STATE_PRESSED);
    lv_obj_set_style_border_color(row, lv_color_hex(COL_EDGE), 0);
    lv_obj_set_style_border_width(row, 2, 0);
    lv_obj_set_style_radius(row, 10, 0);
    lv_obj_set_style_pad_hor(row, 14, 0);
    lv_obj_set_style_pad_ver(row, 8, 0);
    lv_obj_set_style_pad_gap(row, 6, 0);
    lv_obj_set_flex_flow(row, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_flex_align(row, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_START);
    lv_obj_set_scrollable(row, false);
    lv_obj_set_clickable(row, tappable);
    if (tappable) {
        lv_obj_add_event_cb(row, on_card, LV_EVENT_CLICKED, (void *)(intptr_t)(bi * PANEL_MAX_CARDS + ci));
    }

    lv_obj_t *name = ui_label(row, font_text_20, c->done ? COL_DIM : COL_TEXT);
    lv_obj_set_width(name, LV_PCT(100));
    lv_label_set_long_mode(name, LV_LABEL_LONG_MODE_DOTS);
    lv_obj_set_height(name, lv_font_get_line_height(font_text_20));
    lv_label_set_text(name, c->name);
    if (c->done) {
        lv_obj_set_style_text_decor(name, LV_TEXT_DECOR_STRIKETHROUGH, 0);
    }

    lv_obj_t *meta = card_plain(row, LV_FLEX_FLOW_ROW, 10);
    lv_obj_set_size(meta, LV_PCT(100), LV_SIZE_CONTENT);
    lv_obj_set_flex_align(meta, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
    lv_obj_t *where = ui_label(meta, font_text_16, c->done ? COL_OK : COL_DIM);
    if (c->done) {
        lv_label_set_text(where, LV_SYMBOL_OK "  Done");
    } else {
        lv_label_set_text(where, c->list);
    }
    if (c->due && !c->done) {
        time_t due = (time_t)c->due, now = time(NULL);
        struct tm tm;
        localtime_r(&due, &tm);
        char buf[24];
        strftime(buf, sizeof buf, "Due %b %e", &tm);
        lv_obj_t *d = ui_label(meta, font_text_16, due < now ? COL_BAD : due - now < 86400 ? COL_WARN : COL_DIM);
        lv_label_set_text(d, buf);
    }
    for (int i = 0; i < c->nlabels; i++) {
        chip(meta, &c->labels[i]);
    }
}

static void build_board(lv_obj_t *tile, const panel_board_t *b, int bi)
{
    lv_obj_t *head = ui_label(tile, font_text_20, COL_TEXT);
    lv_label_set_text(head, b->name);
    if (b->ncards == 0) {
        lv_obj_t *l = ui_label(tile, font_text_16, COL_DIM);
        lv_label_set_text(l, "No cards yet.");
    }
    for (int ci = 0; ci < b->ncards; ci++) {
        card_row(tile, b, bi, ci);
    }
}

// Rebuild what changed: the latest data with pending dones applied, compared with what's shown.
static void render(void)
{
    int n = latest ? latest->nboards : 0;
    memcpy(next, latest ? latest->boards : next, sizeof next[0] * n);
    for (int b = 0; b < n; b++) {
        for (int c = 0; c < next[b].ncards; c++) {
            next[b].cards[c].done |= find_pending(next[b].cards[c].id) != NULL;
        }
    }
    if (n == nshown && !memcmp(next, shown, sizeof next[0] * n)) {
        return; // nothing visible changed (the GPU numbers churn every poll)
    }
    memcpy(shown, next, sizeof shown[0] * n);

    lv_label_set_text(message, n ? "" : "No boards yet.\nAdd the deskpanel user to a Planka board to show it here.");
    lv_obj_set_hidden(message, n != 0);
    if (n != nshown) {
        lv_obj_clean(tv);
        lv_obj_clean(dots);
        for (int b = 0; b < n; b++) {
            lv_dir_t dir = (b > 0 ? LV_DIR_TOP : LV_DIR_NONE) | (b < n - 1 ? LV_DIR_BOTTOM : LV_DIR_NONE);
            lv_obj_t *tile = lv_tileview_add_tile(tv, 0, (uint8_t)b, dir);
            lv_obj_set_flex_flow(tile, LV_FLEX_FLOW_COLUMN);
            lv_obj_set_style_pad_gap(tile, 8, 0);
            lv_obj_t *dot = lv_obj_create(dots);
            lv_obj_remove_style_all(dot);
            lv_obj_set_size(dot, 10, 10);
            lv_obj_set_style_radius(dot, LV_RADIUS_CIRCLE, 0);
            lv_obj_set_style_bg_opa(dot, LV_OPA_COVER, 0);
        }
        lv_obj_set_hidden(dots, n < 2);
        nshown = n;
        current = current < n ? current : 0;
        if (n) {
            lv_tileview_set_tile_by_index(tv, 0, (uint32_t)current, LV_ANIM_OFF);
        }
    }
    for (int b = 0; b < n; b++) {
        lv_obj_t *tile = lv_obj_get_child(tv, b);
        lv_obj_clean(tile);
        build_board(tile, &shown[b], b);
    }
    update_dots();
}

// Results of mark-done POSTs: keep the optimistic state on success, roll back on failure.
static void poll_results(lv_timer_t *t)
{
    net_done_t r;
    while (net_done_result(&r)) {
        pending_t *p = find_pending(r.card_id);
        if (r.status >= 200 && r.status < 300) {
            ui_toast(LV_SYMBOL_OK "  Marked done", COL_OK);
            continue; // stays pending until the data shows it in the done list
        }
        if (p) {
            p->id[0] = '\0';
        }
        char msg[64];
        if (r.status < 0) {
            snprintf(msg, sizeof msg, "Couldn't reach the aggregator - not done");
        } else {
            snprintf(msg, sizeof msg, "Couldn't mark done (HTTP %d)", r.status);
        }
        ui_toast(msg, COL_BAD);
        render();
    }
}

static void advance(lv_timer_t *t)
{
    if (nshown < 2 || ui_sheet_is_open() || now_us() - touched_us < IDLE_MS * 1000LL) {
        return;
    }
    lv_tileview_set_tile_by_index(tv, 0, (uint32_t)((current + 1) % nshown), LV_ANIM_ON);
}

static void create(card_t *c)
{
    if (owner) { // one board card: a second would only repeat it
        ui_text(card_note(c), "already on another tab");
        return;
    }
    owner = c;
    nshown = -1; // new objects: render() builds every tile
    lv_obj_t *parent = c->box;
    lv_obj_set_style_pad_all(parent, 12, 0);
    lv_obj_set_scrollable(parent, false);
    root = card_plain(parent, LV_FLEX_FLOW_ROW, 10);
    lv_obj_set_size(root, LV_PCT(100), LV_PCT(100));
    lv_obj_set_flex_align(root, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);

    tv = lv_tileview_create(root);
    lv_obj_set_height(tv, LV_PCT(100));
    lv_obj_set_flex_grow(tv, 1);
    lv_obj_set_style_bg_opa(tv, LV_OPA_TRANSP, 0);
    lv_obj_set_scrollbar_mode(tv, LV_SCROLLBAR_MODE_OFF);
    lv_obj_add_event_cb(tv, on_tile, LV_EVENT_VALUE_CHANGED, NULL);
    lv_obj_add_event_cb(tv, on_scroll, LV_EVENT_SCROLL_BEGIN, NULL);

    dots = card_plain(root, LV_FLEX_FLOW_COLUMN, 10);
    lv_obj_set_size(dots, 10, LV_SIZE_CONTENT);

    message = ui_label(parent, font_text_20, COL_DIM); // never a blank screen
    lv_obj_set_style_text_align(message, LV_TEXT_ALIGN_CENTER, 0);
    lv_label_set_text(message, "Waiting for the aggregator...");
    lv_obj_center(message);

    results = lv_timer_create(poll_results, 250, NULL);
    advancer = lv_timer_create(advance, ADVANCE_MS, NULL);
}

static void update(card_t *k, const panel_t *p)
{
    if (owner != k) {
        return;
    }
    latest = p;
    lv_obj_set_style_opa(root, p->planka_stale ? LV_OPA_60 : LV_OPA_COVER, 0);
    // Forget pending dones once the data agrees (card now in the done list) or after PENDING_MS.
    for (int i = 0; i < MAX_PENDING; i++) {
        if (!pending[i].id[0]) {
            continue;
        }
        bool confirmed = false;
        for (int b = 0; b < p->nboards; b++) {
            for (int c = 0; c < p->boards[b].ncards; c++) {
                confirmed |= p->boards[b].cards[c].done && !strcmp(p->boards[b].cards[c].id, pending[i].id);
            }
        }
        if (confirmed || now_us() - pending[i].since_us > PENDING_MS * 1000LL) {
            pending[i].id[0] = '\0';
        }
    }
    render();
}

static void destroy(card_t *c)
{
    if (owner == c) { // ui.c deletes the objects next; a mark-done result waits for the next board card
        lv_timer_delete(results);
        lv_timer_delete(advancer);
        owner = NULL;
    }
}

const card_type_t card_board = {"board", create, update, destroy, NULL};
