// The aggregator's /api/v1/astro document (sun and moon times, phase, what's overhead) as a plain struct.
// Plain C + cJSON, no ESP-IDF, so test/astro_test.c can run it on a PC.
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define ASTRO_OVER_MAX 12

#define SKY_W     378
#define SKY_H     378
#define SKY_BYTES (SKY_W * SKY_H * 2) // raw little-endian RGB565, 285,768

// The panel's pixels are wider than tall (a 16:9 glass carrying 800 x 480): pixel width / pixel height. Everything
// round on the clock is drawn this much narrower so it's round on the glass. To retune: measure the dial's outer
// ring on the glass and multiply PANEL_ASPECT by its width_mm / height_mm (it reads 1.00 when tuned).
#define PANEL_ASPECT 1.0587f
#define SKY_FIT_W    ((int)(SKY_W / PANEL_ASPECT + 0.5f)) // the fitted sky's width: lroundf(SKY_W / PANEL_ASPECT), 357
_Static_assert(SKY_FIT_W <= SKY_W, "a PANEL_ASPECT below 1 would make astro_sky_fit() write past a row");
#define DIAL_R 230 // the clock's dial: its outer radius, physical pixels

// Fits a SKY_W x SKY_H RGB565 sky picture to the panel's pixels, in place, once per new picture: each row is
// resampled (bilinear on each 5/6/5 channel) into its first SKY_FIT_W columns, the source's middle (189.0) on the
// fitted 178.0 (the view puts that on the dial's centre). The stride stays SKY_W; the columns past SKY_FIT_W are
// left as they were. Everything outside DIAL_R, in physical pixels, is painted the screen's #03050c.
void astro_sky_fit(uint16_t *px);

typedef struct {
    long long sky; // unix time of the current sky picture, 0 before the first render
    char rise[12], set[12], daylight[16], moonrise[12], moonset[12], phase[24], label[24];
    float illum;   // 0..1, -1 when absent
    bool waxing;
    int day0, day1; // sunrise, sunset as minute-of-local-day; -1/-1 for polar day or night (or absent)
    int nover;
    char over[ASTRO_OVER_MAX][24];
    bool stale;
} astro_t;

// False only if the document is not an object. *out is zeroed first; strings are cut to the field size on a
// UTF-8 boundary and overhead names past ASTRO_OVER_MAX are dropped.
bool astro_parse(const char *json, size_t len, astro_t *out);
