// The aggregator's /api/v1/layout document (which tabs, which cards where) as plain structs.
// Plain C + cJSON, no ESP-IDF, so test/layout_test.c can run it on a PC.
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define LAYOUT_MAX_TABS  6
#define LAYOUT_MAX_CARDS 24

typedef struct {
    char id[24], type[12], src[28], title[28], tap[24];
    int col, row, w, h;
    char gauge[8], line1[8], as[8], line2[2][8]; // gpu cards' "slots" (empty when absent); 40 bytes keeps the struct padding-free
} layout_card_t;

typedef struct {
    char name[16];
    int ncards;
    layout_card_t cards[LAYOUT_MAX_CARDS];
} layout_tab_t;

typedef struct {
    int ntabs;
    layout_tab_t tabs[LAYOUT_MAX_TABS];
    // The idle tour ("tour" block; missing block or key -> true, 5 min, 60 s, 20 s, true, 0).
    uint32_t tour_idle_ms, tour_clock_ms, tour_tab_ms;
    bool tour_on, tour_clock;
    uint8_t tour_skip; // bit k = tab k is skipped
    uint8_t tour_pad;  // (always 0) keeps the struct padding-free for layout_equal
} layout_t;

// False only if the document is not a JSON object with a tabs array. Extra tabs/cards are dropped; a card
// whose "at" or "size" is not an array of two numbers is skipped (it cannot be placed).
bool layout_parse(const char *json, size_t len, layout_t *out);
// Byte compare (memcmp): exact for a parse result or a struct copy of one; NOT for a hand-built, stack or
// uninitialised layout_t (padding/garbage past the NULs would differ).
// The aggregator (aggregator/layout.go maxTabName/maxSrcLen/maxTitle/maxTapLen) rejects values longer than the
// array sizes above minus one, so nothing is truncated in practice; keep both sides in sync.
bool layout_equal(const layout_t *a, const layout_t *b);
