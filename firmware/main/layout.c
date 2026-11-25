#include "layout.h"

#include <string.h>

#include "json_util.h"

// [a, b] with both numbers -> true.
static bool pair(const cJSON *o, const char *k, int *a, int *b)
{
    const cJSON *v = js_get(o, k);
    if (!cJSON_IsArray(v) || cJSON_GetArraySize(v) != 2) {
        return false;
    }
    const cJSON *x = v->child, *y = x->next;
    if (!cJSON_IsNumber(x) || !cJSON_IsNumber(y)) {
        return false;
    }
    *a = x->valueint;
    *b = y->valueint;
    return true;
}

// Seconds -> clamped ms; absent or not a number -> def_ms.
static uint32_t secs(const cJSON *o, const char *k, uint32_t def_ms, double lo_s, double hi_s)
{
    const cJSON *v = js_get(o, k);
    if (!cJSON_IsNumber(v)) {
        return def_ms;
    }
    double s = v->valuedouble;
    return (uint32_t)((s < lo_s ? lo_s : s > hi_s ? hi_s : s) * 1000);
}

static void parse_tour(const cJSON *t, layout_t *out)
{
    out->tour_on = out->tour_clock = true;
    out->tour_idle_ms = secs(t, "idle_s", 300000, 60, 7200);
    out->tour_clock_ms = secs(t, "clock_s", 60000, 5, 600);
    out->tour_tab_ms = secs(t, "tab_s", 20000, 5, 600);
    if (cJSON_IsBool(js_get(t, "on"))) {
        out->tour_on = js_yes(t, "on");
    }
    if (cJSON_IsBool(js_get(t, "clock"))) {
        out->tour_clock = js_yes(t, "clock");
    }
    for (const cJSON *s = js_first(t, "skip"); s; s = s->next) {
        if (cJSON_IsNumber(s) && s->valuedouble >= 0 && s->valuedouble < LAYOUT_MAX_TABS) {
            out->tour_skip |= (uint8_t)(1u << s->valueint);
        }
    }
}

bool layout_parse(const char *json, size_t len, layout_t *out)
{
    memset(out, 0, sizeof *out);
    cJSON *root = cJSON_ParseWithLength(json, len);
    if (!cJSON_IsObject(root) || !cJSON_IsArray(js_get(root, "tabs"))) {
        cJSON_Delete(root);
        return false;
    }
    for (const cJSON *t = js_first(root, "tabs"); t && out->ntabs < LAYOUT_MAX_TABS; t = t->next) {
        if (!cJSON_IsObject(t)) {
            continue;
        }
        layout_tab_t *tab = &out->tabs[out->ntabs++];
        js_str(tab->name, sizeof tab->name, t, "name");
        for (const cJSON *c = js_first(t, "cards"); c && tab->ncards < LAYOUT_MAX_CARDS; c = c->next) {
            layout_card_t *lc = &tab->cards[tab->ncards];
            if (!cJSON_IsObject(c) || !pair(c, "at", &lc->col, &lc->row) || !pair(c, "size", &lc->w, &lc->h)) {
                memset(lc, 0, sizeof *lc);
                continue;
            }
            js_str(lc->id, sizeof lc->id, c, "id");
            js_str(lc->type, sizeof lc->type, c, "type");
            js_str(lc->src, sizeof lc->src, c, "src");
            js_str(lc->title, sizeof lc->title, c, "title");
            js_str(lc->tap, sizeof lc->tap, c, "tap");
            const cJSON *sl = js_get(c, "slots");
            js_str(lc->gauge, sizeof lc->gauge, sl, "gauge");
            js_str(lc->line1, sizeof lc->line1, sl, "line1");
            js_str(lc->as, sizeof lc->as, sl, "as");
            int k = 0;
            for (const cJSON *s = js_first(sl, "line2"); s && k < 2; s = s->next) {
                if (cJSON_IsString(s)) {
                    cut_copy(lc->line2[k++], sizeof lc->line2[0], s->valuestring);
                }
            }
            tab->ncards++;
        }
    }
    parse_tour(js_get(root, "tour"), out); // (NULL-safe: no block gives the defaults)
    cJSON_Delete(root);
    return true;
}

bool layout_equal(const layout_t *a, const layout_t *b) { return memcmp(a, b, sizeof *a) == 0; }
