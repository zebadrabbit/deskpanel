// Look and helpers the Homelab cards share (they came out of the old page_homelab.c).
#pragma once

#include "card.h"

#define TEMP_WARN 80 // °C
#define TEMP_BAD  90
#define FULL_WARN 85 // % used
#define FULL_BAD  95

// A transparent, padding-free flex container. Not clickable, so a tap anywhere on a card reaches the
// card (a clickable header row would swallow it: found by the PC simulator).
lv_obj_t *card_plain(lv_obj_t *parent, lv_flex_flow_t flow, int gap);
void card_tile(lv_obj_t *box);          // the tile look: dark fill, 2 px edge, rounded, a flex column
lv_obj_t *card_head(lv_obj_t *box);     // a full-width row: left text grows, right item hugs the edge
lv_obj_t *card_bar(lv_obj_t *parent, int h);
void card_edge(lv_obj_t *box, uint32_t color);
// Sets a colour style (LV_STYLE_TEXT_COLOR, ...) only when it changes: LVGL redraws an object on every set,
// even an unchanged one, and redraw cost makes the RGB panel drift.
void card_color(lv_obj_t *o, lv_style_prop_t prop, lv_part_t part, uint32_t color);
uint32_t card_level(int pct, int warn, int bad, uint32_t normal); // normal, then COL_WARN, then COL_BAD
void card_gib(char *buf, size_t n, int gib);                       // "512 GiB" / "1.5 TiB"
int card_severity(const char *level);   // 0 INFO/NOTICE, 1 WARNING .. 5 EMERGENCY

// The card's stand-in text, centred over it: "—" until the first panel document, then hidden while the
// source is found, or "no <kind> '<src>'" while it isn't. card_note() goes last in create(): it hides the
// box's other children until card_found() (on each update; returns found) shows them. On a 1-row card it
// stays on one line, cut with "...".
lv_obj_t *card_note(card_t *c);
bool card_found(card_t *c, lv_obj_t *note, bool found, const char *kind);
void card_opa(card_t *c, bool stale, bool off); // homelab_stale and "agent offline" dim the card

// General cards (number, gauge, bar, status), fed by values() from /api/v1/cards.
// A full-width label that stays on one line, cut with "..." (its height is one line of its font).
lv_obj_t *card_line(lv_obj_t *parent, const lv_font_t *font, uint32_t color);
// Greys the card while stale (the flag, or st CV_STALE), colours its edge amber/red for warn/bad, and returns
// the main value's colour: COL_TEXT, COL_WARN, COL_BAD, or COL_DIM with no value (v NULL or CV_NONE).
uint32_t card_state(card_t *c, const card_val_t *v, bool stale);
// What the main value says: v->v, or with no value an em dash; with `reason` (a size that has no sub-line)
// the aggregator's reason for it instead, when it sent one.
const char *card_value(const card_val_t *v, bool reason);

// The last hour's plot (the graph card, the GPU card's graph slot).
#define CARD_PTS 60 // S4: at most 60 points a line
// n lines (line 1 accent blue, line 2 violet) drawn straight from y[k] (CARD_PTS points 0..63, oldest first;
// LV_CHART_POINT_NONE draws nothing), full width, growing into the room its flex parent leaves. No tile,
// border, dots or division lines; 2 px lines with square ends; a tap on it is a tap on the card.
lv_obj_t *card_plot(lv_obj_t *parent, int32_t (*y)[CARD_PTS], int n);
// Puts a graph string's points (S4: base64url, a short one right-aligned, others skipped) in y, only when s
// differs from `have` (CARD_PTS + 1 bytes: the string y holds). True when y changed: lv_chart_refresh() the
// plot then, so an unchanged graph costs no redraw.
bool card_points(int32_t *y, char *have, const char *s);
