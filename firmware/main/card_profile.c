// Profile card (1x1, 2x1): a button that sets every light card to what was saved under it. Filled once it has
// saved settings; an outline with a dim title until then (the aggregator's sub "not saved"). A press applies it
// (or says how to save); a long press offers to save the lights as they are now. Results come back through
// card_light.c's reader (card_profile_result).
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "card_util.h"
#include "net.h"

#define BRASS 0xd8b56a

typedef struct profile {
    struct profile *next; // every profile card on screen
    card_t *c;
    lv_obj_t *title;
    bool unsaved;
} profile_t;

static profile_t *profiles;
static char sheet_id[24];

// "<before><the card's title><after>" as a toast; the card may have gone since (a layout change): then its id.
static void toast(const char *before, const char *id, const char *after, uint32_t color)
{
    profile_t *p = profiles;
    while (p && strcmp(p->c->def->id, id)) {
        p = p->next;
    }
    char buf[80];
    snprintf(buf, sizeof buf, "%s%s%s", before, p ? p->c->def->title : id, after);
    ui_toast(buf, color);
}

static void hold_to_save(const char *id) { toast("Hold to save the current lights as ", id, "", COL_ACCENT); }

static void send(const char *id, const char *action)
{
    if (!net_profile(id, action)) {
        ui_toast("Busy - try again in a moment", COL_BAD);
    } else if (!strcmp(action, "apply")) {
        toast("", id, "", BRASS);
    }
}

void card_profile_result(const char *id, const char *action, int status)
{
    bool save = !strcmp(action, "save");
    if (status >= 200 && status < 300) {
        if (save) {
            toast("Saved ", id, "", COL_OK);
        }
    } else if (status == 409 && save) {
        ui_toast("No light cards to save", COL_BAD);
    } else if (status == 409) { // not saved (the tile didn't know yet)
        hold_to_save(id);
    } else {
        ui_toast(card_act_error(status), COL_BAD);
    }
}

static void on_tap(lv_event_t *e)
{
    profile_t *p = lv_event_get_user_data(e);
    if (p->unsaved) {
        hold_to_save(p->c->def->id);
    } else {
        send(p->c->def->id, "apply");
    }
}

static void on_cancel(lv_event_t *e) { ui_sheet_close(); }

static void on_save(lv_event_t *e)
{
    send(sheet_id, "save");
    ui_sheet_close();
}

static void on_hold(lv_event_t *e)
{
    profile_t *p = lv_event_get_user_data(e);
    char buf[80];
    snprintf(buf, sizeof buf, "Save the current lights as %s?", p->c->def->title);
    strlcpy(sheet_id, p->c->def->id, sizeof sheet_id);
    lv_obj_t *box = ui_sheet(buf);
    lv_obj_t *l = ui_label(box, font_text_20, COL_TEXT);
    lv_obj_set_width(l, LV_PCT(100));
    lv_label_set_text(l, p->unsaved ? "Every light card is stored as it is right now. After that, a press on this button sets them all again."
                                    : "Every light card is stored as it is right now, in place of what this button set before.");
    lv_obj_t *row = card_plain(box, LV_FLEX_FLOW_ROW, 16);
    lv_obj_set_size(row, LV_PCT(100), 64);
    ui_button(row, "Cancel", COL_EDGE, COL_TEXT, on_cancel);
    ui_button(row, "Save", BRASS, COL_BG, on_save);
}

static void create(card_t *c)
{
    profile_t *p = c->state = calloc(1, sizeof *p);
    p->c = c;
    p->next = profiles;
    profiles = p;
    card_acts_start();
    lv_obj_set_clickable(c->box, true);
    lv_obj_set_style_bg_color(c->box, lv_color_hex(COL_BG), LV_STATE_PRESSED);
    // Never LV_EVENT_CLICKED: LVGL sends that after a long press too.
    lv_obj_add_event_cb(c->box, on_tap, LV_EVENT_SHORT_CLICKED, p);
    lv_obj_add_event_cb(c->box, on_hold, LV_EVENT_LONG_PRESSED, p);
    lv_obj_set_flex_align(c->box, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
    p->title = card_line(c->box, c->w >= 2 ? font_text_20 : font_text_16, COL_TEXT);
    lv_obj_set_style_text_align(p->title, LV_TEXT_ALIGN_CENTER, 0);
    ui_text(p->title, c->def->title);
}

static void values(card_t *c, const card_val_t *v, bool stale)
{
    profile_t *p = c->state;
    card_opa(c, stale, false);
    p->unsaved = v && !strcmp(v->sub, "not saved"); // no entry: unknown, a press asks the aggregator
    bool filled = v && !p->unsaved;
    card_color(c->box, LV_STYLE_BG_COLOR, LV_PART_MAIN, filled ? COL_EDGE : COL_TILE);
    card_color(p->title, LV_STYLE_TEXT_COLOR, LV_PART_MAIN, filled ? COL_TEXT : COL_DIM);
}

static void destroy(card_t *c)
{
    for (profile_t **at = &profiles; *at; at = &(*at)->next) {
        if (*at == c->state) {
            *at = (*at)->next;
            break;
        }
    }
    free(c->state);
}

const card_type_t card_profile = {"profile", create, NULL, destroy, values};
