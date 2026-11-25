// Dots card (src: "tailscale", "services" or "both", the default): status dots per source. Green =
// up/online. Down services are red (they should be up); offline Tailscale peers are just grey (devices
// come and go). What doesn't fit becomes "+N", red when a hidden service is down. By size:
//   4x1  a titled group per source on one line, each ending in its own "+N"; each gets what it needs, up to half
//   4x2  a line per source; one source wraps onto both lines
//   2x1  one line: the items left to right from the first source that has any, then one "+N" (titles
//        only if everything fits)
// Rebuilt only when the items change.
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include "card_util.h"

#define GROUP_GAP 28
#define ITEM_GAP  14
#define LINE_GAP  20 // 4x2: between its two lines

typedef struct {
    panel_peer_t peers[PANEL_MAX_PEERS], svcs[PANEL_MAX_SVCS]; // on screen
    int npeers, nsvcs;                                         // -1: nothing shown yet
} dots_t;

typedef struct {
    const char *title;
    const panel_peer_t *items;
    int n;
    bool alarm; // a down item is red, and so is a "+N" hiding one
} source_t;

// A row of dots. Children: per source its title, then one item per entry; last the "+N" label. An item
// is a row (dot and name), a title a label (a later one GROUP_GAP from the items before it); an item's
// user data is non-NULL if it's a down service.
static lv_obj_t *group(lv_obj_t *box, const source_t *s, int ns)
{
    lv_obj_t *g = card_plain(box, LV_FLEX_FLOW_ROW, ITEM_GAP);
    lv_obj_set_size(g, LV_SIZE_CONTENT, LV_SIZE_CONTENT);
    lv_obj_set_flex_align(g, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
    int total = 0;
    for (int j = 0; j < ns; j++, s++) {
        lv_obj_t *title = ui_label(g, &lv_font_montserrat_16, COL_DIM);
        lv_label_set_text(title, s->title);
        if (j) { // a later source keeps the groups' distance (2x1)
            lv_obj_set_style_margin_left(title, GROUP_GAP - ITEM_GAP, 0);
        }
        for (int i = 0; i < s->n; i++) {
            lv_obj_t *item = card_plain(g, LV_FLEX_FLOW_ROW, 6);
            lv_obj_set_size(item, LV_SIZE_CONTENT, LV_SIZE_CONTENT);
            lv_obj_set_flex_align(item, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
            lv_obj_set_user_data(item, (void *)(uintptr_t)(s->alarm && !s->items[i].on));
            lv_obj_t *dot = lv_obj_create(item);
            lv_obj_remove_style_all(dot);
            lv_obj_set_clickable(dot, false); // a tap on the dot is a tap on the card
            lv_obj_set_size(dot, 12, 12);
            lv_obj_set_style_radius(dot, LV_RADIUS_CIRCLE, 0);
            lv_obj_set_style_bg_opa(dot, LV_OPA_COVER, 0);
            lv_obj_set_style_bg_color(dot, lv_color_hex(s->items[i].on ? COL_OK : s->alarm ? COL_BAD : COL_EDGE), 0);
            lv_obj_t *name = ui_label(item, font_text_16, s->items[i].on ? COL_TEXT : COL_DIM);
            lv_label_set_text(name, s->items[i].name);
        }
        total += s->n;
    }
    lv_label_set_text_fmt(ui_label(g, &lv_font_montserrat_16, COL_DIM), "+%d", total); // as wide as it gets
    return g;
}

static bool is_title(lv_obj_t *o) { return lv_obj_check_type(o, &lv_label_class); }

static int natural(lv_obj_t *g) // every item, no "+N"
{
    int n = (int)lv_obj_get_child_count(g) - 1, w = 0;
    for (int i = 0; i < n; i++) {
        w += (i ? ITEM_GAP : 0) + lv_obj_get_width(lv_obj_get_child(g, i));
    }
    return w;
}

// Would the first k of n items (with the titles, if kept, and "+N" if items are left over) fit on
// `lines` lines of `room` px? Wraps the way LVGL's flex does: what doesn't fit starts a new line.
static bool fits(lv_obj_t *g, int k, int n, int room, int lines, bool titles)
{
    int last = (int)lv_obj_get_child_count(g) - 1, x = 0, line = 1, item = 0;
    for (int i = 0; i <= last; i++) {
        lv_obj_t *o = lv_obj_get_child(g, i);
        if (!(i == last ? k < n : is_title(o) ? titles : item++ < k)) {
            continue;
        }
        int w = lv_obj_get_width(o) + lv_obj_get_style_margin_left(o, 0);
        if (x + w > room) {
            if (!x || ++line > lines) {
                return false;
            }
            x = 0;
        }
        x += w + ITEM_GAP;
    }
    return true;
}

// Keep the items that fit on `lines` lines of `room` px; the rest become "+N". drop_titles: the titles go
// too unless everything fits with them.
static void fit(lv_obj_t *g, int room, int lines, bool drop_titles)
{
    int last = (int)lv_obj_get_child_count(g) - 1, n = 0;
    for (int i = 0; i < last; i++) {
        n += !is_title(lv_obj_get_child(g, i));
    }
    bool titles = !drop_titles || fits(g, n, n, room, lines, true);
    int shown = n;
    while (shown > 0 && !fits(g, shown, n, room, lines, titles)) { // ponytail: O(n^2), n <= 22
        shown--;
    }
    bool down = false;
    for (int i = 0, item = 0; i < last; i++) {
        lv_obj_t *o = lv_obj_get_child(g, i);
        bool title = is_title(o), hide = title ? !titles : item++ >= shown;
        lv_obj_set_hidden(o, hide);
        down |= hide && !title && lv_obj_get_user_data(o);
    }
    lv_obj_t *more = lv_obj_get_child(g, last);
    lv_label_set_text_fmt(more, "+%d", n - shown);
    lv_obj_set_style_text_color(more, lv_color_hex(down ? COL_BAD : COL_DIM), 0);
    lv_obj_set_hidden(more, shown == n);
}

static void create(card_t *c)
{
    dots_t *d = c->state = calloc(1, sizeof *d);
    d->npeers = d->nsvcs = -1;
    if (c->h >= 2) { // a line per group
        lv_obj_set_flex_align(c->box, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_START);
        lv_obj_set_style_pad_row(c->box, LINE_GAP, 0);
    } else {
        lv_obj_set_flex_flow(c->box, LV_FLEX_FLOW_ROW);
        lv_obj_set_flex_align(c->box, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
        lv_obj_set_style_pad_column(c->box, GROUP_GAP, 0); // between the Tailscale and Services groups
    }
    lv_label_set_text(ui_label(c->box, font_text_16, COL_DIM), "\xe2\x80\x94"); // em dash: no data yet
}

static void update(card_t *c, const panel_t *p)
{
    dots_t *d = c->state;
    card_opa(c, p->homelab_stale, false);
    bool ts = strcmp(c->def->src, "services") != 0, sv = strcmp(c->def->src, "tailscale") != 0;
    int np = ts ? p->npeers : 0, ns = sv ? p->nsvcs : 0;
    if (np == d->npeers && ns == d->nsvcs && !memcmp(d->peers, p->peers, sizeof p->peers[0] * np) &&
        !memcmp(d->svcs, p->svcs, sizeof p->svcs[0] * ns)) {
        return;
    }
    lv_obj_clean(c->box);
    source_t s[2] = {{"TAILSCALE", p->peers, np, false}, {"SERVICES", p->svcs, ns, true}};
    if (c->w < 4) { // 2x1: the sources that have items, as one row (just the titles if none has any)
        source_t use[2];
        int k = 0;
        for (int i = 0; i < 2; i++) {
            if ((i ? sv : ts) && (s[i].n || !(np + ns))) {
                use[k++] = s[i];
            }
        }
        lv_obj_t *g = group(c->box, use, k);
        lv_obj_update_layout(c->box);
        fit(g, lv_obj_get_content_width(c->box), 1, true);
    } else {
        lv_obj_t *a = ts ? group(c->box, &s[0], 1) : NULL;
        lv_obj_t *b = sv && (ns || !ts) ? group(c->box, &s[1], 1) : NULL;
        lv_obj_update_layout(c->box);
        int room = lv_obj_get_content_width(c->box);
        if (a && b && c->h >= 2) { // 4x2: a line each
            fit(a, room, 1, false);
            fit(b, room, 1, false);
        } else if (a && b) {
            int wa = natural(a), wb = natural(b), r = room - GROUP_GAP, half = r / 2;
            int ra = wa + wb <= r ? wa : wb <= half ? r - wb : wa <= half ? wa : half;
            fit(a, ra, 1, false);
            fit(b, r - ra, 1, false);
        } else if (a || b) {
            lv_obj_t *g = a ? a : b;
            if (c->h >= 2) { // 4x2: wraps onto the second line
                lv_obj_set_width(g, room);
                lv_obj_set_flex_flow(g, LV_FLEX_FLOW_ROW_WRAP);
                lv_obj_set_style_pad_row(g, LINE_GAP, 0);
            }
            fit(g, room, c->h >= 2 ? 2 : 1, false);
        }
    }
    memcpy(d->peers, p->peers, sizeof p->peers);
    memcpy(d->svcs, p->svcs, sizeof p->svcs);
    d->npeers = np;
    d->nsvcs = ns;
}

static void destroy(card_t *c) { free(c->state); }

const card_type_t card_dots = {"dots", create, update, destroy, NULL};
