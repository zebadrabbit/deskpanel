// JSON -> weather_t, alert text, takeover acks. Plain C + cJSON (see weather.h).
#include "weather.h"

#include <ctype.h>
#include <stdio.h>
#include <string.h>
#include <time.h>

#include "json_util.h"

bool weather_parse(const char *json, size_t len, weather_t *w)
{
    memset(w, 0, sizeof *w);
    cJSON *root = cJSON_ParseWithLength(json, len);
    if (!cJSON_IsObject(root)) {
        cJSON_Delete(root);
        return false;
    }
    w->stale = js_yes(root, "stale");
    w->updated_at = js_big(root, "updated_at");

    const cJSON *n = js_get(root, "now"); // null before the first collection
    if (cJSON_IsObject(n)) {
        w->has_now = true;
        w->now.t = js_num(n, "t");
        w->now.rh = js_num(n, "rh");
        w->now.code = js_num(n, "code");
        w->now.hi = js_num(n, "hi");
        w->now.lo = js_num(n, "lo");
        w->now.day = js_yes(n, "day");
        js_str(w->now.text, sizeof w->now.text, n, "text");
        js_str(w->now.wind, sizeof w->now.wind, n, "wind");
    }
    for (const cJSON *f = js_first(root, "frames"); f && w->nframes < WX_FRAMES; f = f->next) {
        w->frames[w->nframes++] = cJSON_IsNumber(f) ? (long long)f->valuedouble : 0;
    }

    const cJSON *al = js_get(root, "alerts"); // every lookup below is NULL-safe
    for (const cJSON *o = js_first(al, "list"); o && w->nalerts < WX_MAX_ALERTS; o = o->next) {
        wx_alert_t *d = &w->alerts[w->nalerts++];
        d->n = js_num(o, "n");
        d->lvl = js_num(o, "lvl");
        d->until = js_big(o, "until");
        d->take = js_yes(o, "take");
        js_str(d->key, sizeof d->key, o, "key");
        js_str(d->event, sizeof d->event, o, "event");
        js_str(d->cls, sizeof d->cls, o, "cls");
    }
    for (const cJSON *o = js_first(al, "polys"); o && w->npolys < WX_MAX_POLYS; o = o->next) {
        wx_poly_t *d = &w->polys[w->npolys++];
        d->kind = js_text(o, "k")[0];
        d->home = js_yes(o, "h");
        for (const cJSON *p = js_first(o, "p"); p && d->npts < WX_MAX_PTS; p = p->next) {
            const cJSON *x = cJSON_IsArray(p) ? p->child : NULL;
            const cJSON *y = x ? x->next : NULL;
            if (cJSON_IsNumber(x) && cJSON_IsNumber(y)) {
                d->pts[d->npts][0] = (int16_t)x->valueint;
                d->pts[d->npts][1] = (int16_t)y->valueint;
                d->npts++;
            }
        }
    }
    cJSON_Delete(root);
    return true;
}

// "2:52 PM" in local time (hand-rolled: %l pads with a space, %P isn't portable).
static void clock12(char *dst, size_t n, long long t)
{
    time_t tt = (time_t)t;
    struct tm tm;
    localtime_r(&tt, &tm);
    snprintf(dst, n, "%d:%02d %s", tm.tm_hour % 12 ? tm.tm_hour % 12 : 12, tm.tm_min, tm.tm_hour < 12 ? "AM" : "PM");
}

size_t wx_markup(const char *para, size_t len, char *out, size_t cap)
{
    if (!cap) {
        return 0;
    }
    size_t s = len >= 2 && para[0] == '*' && para[1] == ' ' ? 2 : 0, e = s, o = 0;
    while (e < len && ((para[e] >= 'A' && para[e] <= 'Z') || para[e] == ' ' || para[e] == '/' || para[e] == '-')) {
        e++;
    }
    if (e - s >= 2 && len - e >= 3 && !memcmp(para + e, "...", 3)) { // the keyword holds no '#'
        int n = snprintf(out, cap, "%.*s#ffd35a %.*s...#", (int)s, para, (int)(e - s), para + s);
        o = n < 0 ? 0 : (size_t)n < cap ? (size_t)n : cap - 1;
        e += 3;
    } else {
        e = 0;
    }
    for (; e < len && o + 2 < cap; e++) {
        if (para[e] == '#') {
            out[o++] = '#'; // "##" draws one '#'
        }
        out[o++] = para[e];
    }
    out[o] = '\0';
    return o;
}

bool weather_alert_text(const char *json, size_t len, char *out, size_t cap, wx_text_t *t)
{
    if (cap == 0) {
        return false;
    }
    cJSON *root = cJSON_ParseWithLength(json, len);
    if (!cJSON_IsObject(root)) {
        cJSON_Delete(root);
        return false;
    }
    long long ts = js_big(root, "sent"), te = js_big(root, "expires");
    char sent[12], exp[12], when[48] = "";
    clock12(sent, sizeof sent, ts);
    clock12(exp, sizeof exp, te);
    if (ts > 0 && te > 0) {
        snprintf(when, sizeof when, "Issued %s \xC2\xB7 expires %s", sent, exp);
    } else if (ts > 0) {
        snprintf(when, sizeof when, "Issued %s", sent);
    } else if (te > 0) {
        snprintf(when, sizeof when, "Expires %s", exp);
    }
    int n = snprintf(out, cap, "%s%c%s%c%s%c%s", js_text(root, "headline"), 0, when, 0, js_text(root, "desc"), 0,
                     js_text(root, "instr"));
    size_t end = n < 0 ? 0 : (size_t)n; // where the last NUL is
    if (end >= cap) {
        // Cut, keeping room for every part's NUL. If the last character is then incomplete, drop it: find the
        // last lead byte and check that its whole sequence fits in what we kept.
        end = cap < 4 ? 0 : cap - 4;
        size_t i = end;
        while (i > 0 && ((unsigned char)out[i - 1] & 0xC0) == 0x80) {
            i--;
        }
        if (i > 0) {
            unsigned char lead = (unsigned char)out[i - 1];
            size_t need = lead >= 0xF0 ? 4 : lead >= 0xE0 ? 3 : lead >= 0xC0 ? 2 : 1;
            if (end - (i - 1) < need) {
                end = i - 1;
            }
        }
        out[end] = '\0';
    }
    const char *p = out, **part[] = {&t->head, &t->when, &t->desc, &t->instr};
    for (int k = 0; k < 4; k++) { // a part cut off points at the last NUL: ""
        *part[k] = p;
        p += strlen(p);
        p += p < out + end;
    }
    cJSON_Delete(root);
    return true;
}

bool weather_alert_matches(const char *json, size_t len, const wx_alert_t *a)
{
    cJSON *root = cJSON_ParseWithLength(json, len);
    if (!cJSON_IsObject(root)) {
        cJSON_Delete(root);
        return false;
    }
    char ev[sizeof a->event];
    js_str(ev, sizeof ev, root, "event"); // cut the way weather_parse cuts it
    char key[sizeof a->key];
    js_str(key, sizeof key, root, "key");
    // the chain key survives an update of the same warning; without one, fall back to the event and end time
    bool same = key[0] && a->key[0] ? !strcmp(key, a->key) : !strcmp(ev, a->event) && js_big(root, "expires") == a->until;
    cJSON_Delete(root);
    return same;
}

static int tornado_lvl(const wx_alert_t *a) // 0..3 for a Tornado Warning, -1 for anything else
{
    return strcmp(a->event, "Tornado Warning") ? -1 : a->lvl < 0 ? 0 : a->lvl > 3 ? 3 : a->lvl;
}

void wx_badge_text(const wx_alert_t *a, const char *until, char *out, size_t cap)
{
    static const char *const head[] = {"TORNADO WARNING", "TORNADO OBSERVED", "DANGEROUS TORNADO", "TORNADO EMERGENCY"};
    char up[sizeof a->event];
    size_t i = 0;
    for (; a->event[i] && i < sizeof up - 1; i++) {
        up[i] = (char)toupper((unsigned char)a->event[i]);
    }
    up[i] = '\0';
    int l = tornado_lvl(a);
    snprintf(out, cap, "%s%s%s", l < 0 ? up : head[l], until[0] ? " \xC2\xB7 " : "", until);
}

void wx_notice_text(const wx_alert_t *a, const char *until, char *out, size_t cap)
{
    static const char *const tail[] = {"", " \xE2\x80\x94 tornado observed", " \xE2\x80\x94 considerable damage threat", ""};
    int l = tornado_lvl(a);
    snprintf(out, cap, "%s for your area%s%s%s. Take shelter now.", l == 3 ? "TORNADO EMERGENCY" : a->event,
             until[0] ? " " : "", until, l < 0 ? "" : tail[l]);
}

int wx_takeover_due(const weather_t *w, const wx_acks_t *acks)
{
    for (int i = 0; i < w->nalerts; i++) {
        if (!w->alerts[i].take) {
            continue;
        }
        bool acked = false;
        for (int j = 0; j < acks->n; j++) {
            if (!strcmp(acks->a[j].key, w->alerts[i].key) && acks->a[j].lvl >= w->alerts[i].lvl) {
                acked = true;
            }
        }
        if (!acked) {
            return i;
        }
    }
    return -1;
}

void wx_ack(wx_acks_t *acks, const char *key, int lvl)
{
    int cap = (int)(sizeof acks->a / sizeof acks->a[0]);
    char k[sizeof acks->a[0].key];
    cut_copy(k, sizeof k, key); // compare the stored (cut) form, as parse stores it
    for (int j = 0; j < acks->n; j++) {
        if (!strcmp(acks->a[j].key, k)) {
            if (lvl > acks->a[j].lvl) {
                acks->a[j].lvl = lvl;
            }
            return;
        }
    }
    if (acks->n == cap) { // full: drop the oldest
        memmove(&acks->a[0], &acks->a[1], (cap - 1) * sizeof acks->a[0]);
        acks->n--;
    }
    memcpy(acks->a[acks->n].key, k, sizeof k);
    acks->a[acks->n++].lvl = lvl;
}

bool wx_frames_publish(int n, unsigned pub_mask, unsigned busy_mask, int nbuf)
{
    unsigned all = (1u << nbuf) - 1;
    return n > 0 || !pub_mask || (busy_mask & all) == all;
}
