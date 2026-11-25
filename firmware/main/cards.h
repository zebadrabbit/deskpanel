// The aggregator's /api/v1/cards document (each card's finished display values and graph points) as plain
// structs. Plain C + cJSON, no ESP-IDF, so test/cards_test.c can run it on a PC.
#pragma once

#include <stdbool.h>
#include <stddef.h>

#include "layout.h"

#define CARDS_MAX (LAYOUT_MAX_TABS * LAYOUT_MAX_CARDS)

typedef enum { CV_OK, CV_WARN, CV_BAD, CV_STALE, CV_NONE } cv_state_t;

typedef struct {
    char id[24];
    cv_state_t st;
    char v[16], v2[16], sub[24];
    float f; // 0..1, -1 when absent
    int th;  // threshold as a point 0..63, -1 when absent
    int ng;  // strings kept in g, 0..2
    char g[2][61]; // point characters (base64url), NUL-terminated
} card_val_t;

typedef struct {
    int n;
    card_val_t c[CARDS_MAX];
} cards_t;

// False only if the document is not an object with a "cards" object. *out is zeroed first; ids come in
// document order and entries past CARDS_MAX are dropped. Strings are cut to the field size on a UTF-8 boundary.
bool cards_parse(const char *json, size_t len, cards_t *out);
// The entry for `id`, or NULL.
const card_val_t *cards_find(const cards_t *cs, const char *id);
// A graph character's value 0..63, or -1 outside A-Z a-z 0-9 - _ (the renderer skips those).
int cards_point(char ch);
