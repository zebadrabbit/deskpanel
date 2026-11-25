// A card is one tile on a tab's 4x6 grid, placed by the aggregator's layout (layout.h). Each card type is a
// module: create() builds inside c->box (ui.c already placed and sized it, with the tile look), update()
// refreshes from the newest panel document, destroy() frees what create() allocated. All on the LVGL task.
// ui.c destroys every card and rebuilds the tabs whenever the layout changes. To add a type: write a
// card_type_t in its own card_*.c and list it in card_type() (ui.c).
#pragma once

#include "cards.h"
#include "layout.h"
#include "page.h"

typedef struct card card_t;
typedef struct {
    const char *type;                                  // "gpu", "pool", "nas-alerts", "dots", "board", "radar"
    void (*create)(card_t *c);                         // build inside c->box (already sized and placed)
    void (*update)(card_t *c, const panel_t *p);       // new panel document; may be NULL
    void (*destroy)(card_t *c);                        // free c->state; stop timers; may be NULL
    // The general cards' feed (/api/v1/cards): v is the card's entry, NULL when the document has none (drawn
    // like CV_NONE); stale is true with no good /cards poll for STALE_MS. Called after a new document, when
    // stale flips, and right after a rebuild. Sets text, values and styles only. May be NULL.
    void (*values)(card_t *c, const card_val_t *v, bool stale);
} card_type_t;
struct card {
    const card_type_t *t; lv_obj_t *box; const layout_card_t *def; // def points into ui.c's current layout copy
    int w, h;        // cells
    void *state;     // the module's own
};
const card_type_t *card_type(const char *type);        // registry lookup; NULL = unknown (shows a dim "unknown card" box)
void card_tap(const card_t *c);                        // runs c->def->tap (notifications | alert-details | tab:<name> | none)

extern const card_type_t card_gpu, card_pool, card_alerts, card_dots, card_board, card_radar;
extern const card_type_t card_number, card_gauge, card_bars, card_status, card_graph; // general: values(), no update()

void nas_alerts_sheet(const panel_t *p); // card_alerts.c: every alert in full (tap alert-details); p may be NULL
