#include "astro.h"

#include <string.h>

#include "json_util.h"

bool astro_parse(const char *json, size_t len, astro_t *out)
{
    memset(out, 0, sizeof *out);
    out->illum = -1.0f;
    out->day0 = out->day1 = -1;
    cJSON *root = cJSON_ParseWithLength(json, len);
    if (!cJSON_IsObject(root)) {
        cJSON_Delete(root);
        return false;
    }
    out->sky = js_big(root, "sky");
    js_str(out->rise, sizeof out->rise, root, "rise");
    js_str(out->set, sizeof out->set, root, "set");
    js_str(out->daylight, sizeof out->daylight, root, "daylight");
    js_str(out->moonrise, sizeof out->moonrise, root, "moonrise");
    js_str(out->moonset, sizeof out->moonset, root, "moonset");
    js_str(out->phase, sizeof out->phase, root, "phase");
    js_str(out->label, sizeof out->label, root, "label");
    const cJSON *il = js_get(root, "illum");
    if (cJSON_IsNumber(il)) {
        out->illum = (float)il->valuedouble;
    }
    out->waxing = js_yes(root, "waxing");
    out->stale = js_yes(root, "stale");
    const cJSON *d0 = js_first(root, "day"), *d1 = d0 ? d0->next : NULL;
    if (cJSON_IsNumber(d0) && cJSON_IsNumber(d1)) {
        out->day0 = d0->valueint;
        out->day1 = d1->valueint;
    }
    for (const cJSON *o = js_first(root, "overhead"); o && out->nover < ASTRO_OVER_MAX; o = o->next) {
        if (cJSON_IsString(o)) {
            cut_copy(out->over[out->nover++], sizeof out->over[0], o->valuestring);
        }
    }
    cJSON_Delete(root);
    return true;
}

#define SCREEN_565 0x0021 // astro_view.c's screen colour #03050c in RGB565

static int lerp(int a, int b, float f) { return (int)(a + (b - a) * f + 0.5f); } // rounded; a, b >= 0

void astro_sky_fit(uint16_t *px)
{
    for (int y = 0; y < SKY_H; y++) {
        uint16_t *row = px + y * SKY_W;
        float dy = y + 0.5f - SKY_H / 2.0f;
        // Left to right in place: column x samples at s >= x + 0.58, so it reads only columns not yet written.
        for (int x = 0; x < SKY_FIT_W; x++) {
            // physical pixels from the fitted 178.0, and the source column there (its middle is 189.0)
            float dx = (x + 0.5f - SKY_FIT_W / 2) * PANEL_ASPECT, s = dx + SKY_W / 2.0f - 0.5f;
            if (dx * dx + dy * dy >= DIAL_R * DIAL_R) {
                row[x] = SCREEN_565; // outside the dial: the square's corners
                continue;
            }
            s = s < 0 ? 0 : s > SKY_W - 1 ? SKY_W - 1 : s;
            int i = (int)s, j = i + 1 < SKY_W ? i + 1 : i;
            float f = s - i;
            uint16_t a = row[i], b = row[j];
            row[x] = (uint16_t)(lerp(a >> 11, b >> 11, f) << 11 | lerp(a >> 5 & 63, b >> 5 & 63, f) << 5 |
                                lerp(a & 31, b & 31, f));
        }
    }
}
