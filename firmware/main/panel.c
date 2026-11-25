// JSON -> panel_t. Plain C + cJSON, no ESP-IDF, so test/panel_test.c can run it on a PC.
#include "panel.h"

#include <stdlib.h>
#include <string.h>

#include "json_util.h"

static void parse_planka(const cJSON *pk, panel_t *p)
{
    p->planka_stale = js_yes(pk, "stale");
    for (const cJSON *b = js_first(pk, "boards"); b && p->nboards < PANEL_MAX_BOARDS; b = b->next) {
        panel_board_t *d = &p->boards[p->nboards++];
        js_str(d->id, sizeof d->id, b, "id");
        js_str(d->name, sizeof d->name, b, "n");
        d->can_done = js_yes(b, "can_done");
        for (const cJSON *c = js_first(b, "cards"); c && d->ncards < PANEL_MAX_CARDS; c = c->next) {
            panel_card_t *k = &d->cards[d->ncards++];
            js_str(k->id, sizeof k->id, c, "id");
            js_str(k->name, sizeof k->name, c, "n");
            js_str(k->list, sizeof k->list, c, "l");
            k->due = js_big(c, "due");
            k->done = js_yes(c, "d");
            for (const cJSON *l = js_first(c, "lb"); l && k->nlabels < PANEL_MAX_LABELS; l = l->next) {
                panel_label_t *lb = &k->labels[k->nlabels++];
                js_str(lb->name, sizeof lb->name, l, "n");
                lb->color = (unsigned)js_num(l, "c");
            }
        }
    }
}

bool panel_parse(const char *json, size_t len, panel_t *p)
{
    memset(p, 0, sizeof *p);
    cJSON *root = cJSON_ParseWithLength(json, len);
    if (!cJSON_IsObject(root)) {
        cJSON_Delete(root);
        return false;
    }
    const cJSON *hl = js_get(root, "homelab"); // every lookup below is NULL-safe
    const cJSON *nas = js_get(hl, "nas");
    p->homelab_stale = js_yes(hl, "stale");
    p->info_alerts = js_num(nas, "info");

    for (const cJSON *o = js_first(nas, "pools"); o && p->npools < PANEL_MAX_POOLS; o = o->next) {
        panel_pool_t *d = &p->pools[p->npools++];
        js_str(d->name, sizeof d->name, o, "n");
        js_str(d->health, sizeof d->health, o, "h");
        d->ok = js_yes(o, "ok");
        d->used_gib = js_num(o, "used");
        d->avail_gib = js_num(o, "avail");
    }
    for (const cJSON *o = js_first(nas, "alerts"); o && p->nalerts < PANEL_MAX_ALERTS; o = o->next) {
        panel_alert_t *d = &p->alerts[p->nalerts++];
        js_str(d->level, sizeof d->level, o, "lvl");
        js_str(d->msg, sizeof d->msg, o, "msg");
    }
    for (const cJSON *o = js_first(hl, "gpu"); o && p->ngpus < PANEL_MAX_GPUS; o = o->next) {
        panel_gpu_t *d = &p->gpus[p->ngpus++];
        js_str(d->host, sizeof d->host, o, "host");
        js_str(d->name, sizeof d->name, o, "n");
        js_str(d->state, sizeof d->state, o, "st");
        d->up = js_yes(o, "up");
        d->temp = js_num(o, "temp");
        d->util = js_num(o, "util");
        d->vram = js_num(o, "vram");
        d->vram_total = js_num(o, "vram_t");
        d->power = js_num(o, "pwr");
        d->procs = js_num(o, "procs");
    }
    for (const cJSON *o = js_first(hl, "ts"); o && p->npeers < PANEL_MAX_PEERS; o = o->next) {
        panel_peer_t *d = &p->peers[p->npeers++];
        js_str(d->name, sizeof d->name, o, "n");
        d->on = js_yes(o, "on");
    }
    for (const cJSON *o = js_first(hl, "svc"); o && p->nsvcs < PANEL_MAX_SVCS; o = o->next) {
        panel_peer_t *d = &p->svcs[p->nsvcs++];
        js_str(d->name, sizeof d->name, o, "n");
        d->on = js_yes(o, "on");
    }
    parse_planka(js_get(root, "planka"), p);
    for (const cJSON *o = js_first(js_get(root, "notify"), "notes"); o && p->nnotes < PANEL_MAX_NOTES; o = o->next) {
        panel_note_t *d = &p->notes[p->nnotes++];
        d->id = js_num(o, "id");
        d->t = js_big(o, "t");
        js_str(d->src, sizeof d->src, o, "src");
        js_str(d->msg, sizeof d->msg, o, "msg");
        js_str(d->lvl, sizeof d->lvl, o, "lvl");
        js_str(d->act, sizeof d->act, o, "act");
        d->read = js_yes(o, "r");
    }
    cJSON_Delete(root);
    return true;
}

const panel_gpu_t *panel_gpu(const panel_t *p, const char *src)
{
    char host[64], cut[sizeof p->gpus[0].host];
    strlcpy(host, src, sizeof host);
    int n = 1; // "host#n": the n-th GPU of that host (1-based)
    char *hash = strrchr(host, '#');
    if (hash && hash[1] && strspn(hash + 1, "0123456789") == strlen(hash + 1)) {
        n = atoi(hash + 1);
        *hash = '\0';
    }
    cut_copy(cut, sizeof cut, host);
    for (int i = 0; i < p->ngpus; i++) {
        if (!strcmp(p->gpus[i].host, cut) && --n == 0) {
            return &p->gpus[i];
        }
    }
    return NULL;
}

const panel_pool_t *panel_pool(const panel_t *p, const char *src)
{
    char cut[sizeof p->pools[0].name];
    cut_copy(cut, sizeof cut, src);
    for (int i = 0; i < p->npools; i++) {
        if (!strcmp(p->pools[i].name, cut)) {
            return &p->pools[i];
        }
    }
    return NULL;
}
