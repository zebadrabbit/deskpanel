// Small cJSON lookup helpers (js_ prefix) shared by panel.c and weather.c. Every lookup is NULL-safe.
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <string.h>

#include "cJSON.h"

static inline const cJSON *js_get(const cJSON *o, const char *k) { return cJSON_GetObjectItemCaseSensitive(o, k); }

static inline const cJSON *js_first(const cJSON *o, const char *k)
{
    const cJSON *a = js_get(o, k);
    return cJSON_IsArray(a) ? a->child : NULL;
}

// Copy a string, cutting on a UTF-8 boundary if it doesn't fit.
static inline void cut_copy(char *dst, size_t n, const char *s)
{
    if (strlcpy(dst, s, n) >= n) {
        size_t cut = n - 1;
        while (cut > 0 && ((unsigned char)s[cut] & 0xC0) == 0x80) { // s[cut] continues a sequence
            cut--;
        }
        dst[cut] = '\0';
    }
}

static inline const char *js_text(const cJSON *o, const char *k)
{
    const cJSON *v = js_get(o, k);
    return cJSON_IsString(v) ? v->valuestring : "";
}

// Copy a string field (empty if missing or not a string).
static inline void js_str(char *dst, size_t n, const cJSON *o, const char *k) { cut_copy(dst, n, js_text(o, k)); }

static inline int js_num(const cJSON *o, const char *k)
{
    const cJSON *v = js_get(o, k);
    return cJSON_IsNumber(v) ? v->valueint : 0;
}

static inline long long js_big(const cJSON *o, const char *k)
{
    const cJSON *v = js_get(o, k);
    return cJSON_IsNumber(v) ? (long long)v->valuedouble : 0;
}

static inline bool js_yes(const cJSON *o, const char *k) { return cJSON_IsTrue(js_get(o, k)); }
