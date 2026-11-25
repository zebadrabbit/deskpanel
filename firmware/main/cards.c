#include "cards.h"

#include <string.h>

#include "json_util.h"

static cv_state_t state(const char *s)
{
    static const char *const names[] = {"ok", "warn", "bad", "stale", "none"};
    for (int i = 1; i < 5; i++) {
        if (!strcmp(s, names[i])) {
            return (cv_state_t)i;
        }
    }
    return CV_OK; // missing or unknown
}

bool cards_parse(const char *json, size_t len, cards_t *out)
{
    memset(out, 0, sizeof *out);
    cJSON *root = cJSON_ParseWithLength(json, len);
    const cJSON *cards = js_get(root, "cards");
    if (!cJSON_IsObject(root) || !cJSON_IsObject(cards)) {
        cJSON_Delete(root);
        return false;
    }
    for (const cJSON *e = cards->child; e && out->n < CARDS_MAX; e = e->next) {
        if (!cJSON_IsObject(e) || !e->string) {
            continue;
        }
        card_val_t *c = &out->c[out->n++];
        cut_copy(c->id, sizeof c->id, e->string);
        c->st = state(js_text(e, "st"));
        js_str(c->v, sizeof c->v, e, "v");
        js_str(c->v2, sizeof c->v2, e, "v2");
        js_str(c->sub, sizeof c->sub, e, "sub");
        const cJSON *f = js_get(e, "f"), *th = js_get(e, "th");
        c->f = cJSON_IsNumber(f) ? (float)f->valuedouble : -1.0f;
        c->th = cJSON_IsNumber(th) ? th->valueint : -1;
        for (const cJSON *g = js_first(e, "g"); g && c->ng < 2; g = g->next) {
            if (cJSON_IsString(g)) {
                cut_copy(c->g[c->ng++], sizeof c->g[0], g->valuestring);
            }
        }
    }
    cJSON_Delete(root);
    return true;
}

const card_val_t *cards_find(const cards_t *cs, const char *id)
{
    for (int i = 0; i < cs->n; i++) {
        if (!strcmp(cs->c[i].id, id)) {
            return &cs->c[i];
        }
    }
    return NULL;
}

int cards_point(char ch)
{
    if (ch >= 'A' && ch <= 'Z') return ch - 'A';
    if (ch >= 'a' && ch <= 'z') return ch - 'a' + 26;
    if (ch >= '0' && ch <= '9') return ch - '0' + 52;
    return ch == '-' ? 62 : ch == '_' ? 63 : -1;
}
