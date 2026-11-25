// Host-side check of astro_parse() and astro_sky_fit(): `sh test/run.sh`.
#include <assert.h>
#include <stdio.h>
#include <string.h>

#include "astro.h"

static uint16_t pic[SKY_W * SKY_H];

// Squared distance in physical pixels from the dial's centre: the fitted picture's column SKY_FIT_W / 2 (178.0),
// which the view places on x 240.0.
static float phys_r2(int x, int y)
{
    float dx = (x + 0.5f - SKY_FIT_W / 2) * PANEL_ASPECT, dy = y + 0.5f - SKY_H / 2.0f;
    return dx * dx + dy * dy;
}

// Red x % 32, green y % 64: each fitted pixel inside the dial is the bilinear sample at
// x = (x' - 177.5) A + 188.5 (the source's middle, 189.0, lands on the fitted 178.0).
static void fit_samples(void)
{
    for (int y = 0; y < SKY_H; y++) {
        for (int x = 0; x < SKY_W; x++) {
            pic[y * SKY_W + x] = (uint16_t)((x % 32) << 11 | (y % 64) << 5);
        }
    }
    astro_sky_fit(pic);
    for (int y = 0; y < SKY_H; y++) {
        for (int x = 0; x < SKY_FIT_W; x++) {
            if (phys_r2(x, y) >= 229.0f * 229) {
                continue; // painted
            }
            float s = (x + 0.5f - SKY_FIT_W / 2) * PANEL_ASPECT + SKY_W / 2.0f - 0.5f;
            s = s < 0 ? 0 : s > SKY_W - 1 ? SKY_W - 1 : s;
            int i = (int)s, j = i + 1 < SKY_W ? i + 1 : i;
            uint16_t p = pic[y * SKY_W + x];
            float d = (p >> 11) - ((i % 32) * (1 - (s - i)) + (j % 32) * (s - i));
            assert(d >= -1 && d <= 1 && (p >> 5 & 63) == y % 64 && (p & 31) == 0);
        }
    }
}

// A white disc of radius 189 on the dial's #040814, fitted: every row's span centred on the fitted 178.0 (on average
// to a quarter pixel), the widest SKY_FIT_W ± 1, the rows SKY_H ± 1, and round (± 1 %) stretched back by PANEL_ASPECT.
static void fit_round(void)
{
    float off = 0; // the rows' summed (twice) offsets from 178.0
    for (int y = 0; y < SKY_H; y++) {
        for (int x = 0; x < SKY_W; x++) {
            float dx = x + 0.5f - SKY_W / 2.0f, dy = y + 0.5f - SKY_H / 2.0f;
            pic[y * SKY_W + x] = dx * dx + dy * dy <= 189 * 189 ? 0xffff : 0x0842;
        }
    }
    astro_sky_fit(pic);
    int w = 0, h = 0;
    for (int y = 0; y < SKY_H; y++) {
        int l = SKY_FIT_W, r = -1;
        for (int x = 0; x < SKY_FIT_W; x++) {
            if (pic[y * SKY_W + x] >> 11 >= 16) { // at least half white
                l = x < l ? x : l;
                r = x;
            }
        }
        if (r >= 0) {
            h++;
            w = r - l + 1 > w ? r - l + 1 : w;
            int twice = l + r + 1 - SKY_FIT_W / 2 * 2; // twice its middle's offset from 178.0
            assert(twice >= -2 && twice <= 2);
            off += twice;
        }
    }
    assert(off / h > -0.5f && off / h < 0.5f);
    float round = (float)w / h * PANEL_ASPECT;
    assert(w >= SKY_FIT_W - 1 && w <= SKY_FIT_W + 1 && h >= SKY_H - 1 && h <= SKY_H + 1);
    assert(round > 0.99f && round < 1.01f);
}

// Outside the dial (physical r DIAL_R, 230) the fit paints the screen's #03050c, RGB565 0x0021; inside it, and in the
// columns past SKY_FIT_W, nothing is painted.
static void fit_outside(void)
{
    for (int i = 0; i < SKY_W * SKY_H; i++) {
        pic[i] = 0xffff;
    }
    astro_sky_fit(pic);
    for (int y = 0; y < SKY_H; y++) {
        for (int x = 0; x < SKY_W; x++) {
            uint16_t p = pic[y * SKY_W + x];
            if (x >= SKY_FIT_W || phys_r2(x, y) <= 229.7f * 229.7f) {
                assert(p == 0xffff);
            } else if (phys_r2(x, y) >= 230.3f * 230.3f) {
                assert(p == 0x0021);
            }
        }
    }
}

static const char doc[] =
    "{\"sky\": 1795139700, \"rise\": \"6:59 AM\", \"set\": \"5:06 PM\", \"daylight\": \"10 h 7 min\","
    " \"moonrise\": \"1:58 PM\", \"moonset\": \"1:32 AM\", \"phase\": \"Waxing gibbous\", \"illum\": 0.76,"
    " \"waxing\": true, \"day\": [419, 1026], \"label\": \"OVERHEAD NOW\","
    " \"overhead\": [\"Andromeda\", \"Pegasus\", \"Cassiopeia\"], \"stale\": false, \"future\": 1}";

int main(void)
{
    static astro_t a;
    assert(astro_parse(doc, strlen(doc), &a));
    assert(a.sky == 1795139700LL && !strcmp(a.rise, "6:59 AM") && !strcmp(a.set, "5:06 PM"));
    assert(!strcmp(a.daylight, "10 h 7 min") && !strcmp(a.moonrise, "1:58 PM") && !strcmp(a.moonset, "1:32 AM"));
    assert(!strcmp(a.phase, "Waxing gibbous") && !strcmp(a.label, "OVERHEAD NOW"));
    assert(a.illum > 0.7599f && a.illum < 0.7601f && a.waxing && !a.stale && a.day0 == 419 && a.day1 == 1026);
    assert(a.nover == 3 && !strcmp(a.over[0], "Andromeda") && !strcmp(a.over[2], "Cassiopeia"));

    // cuts: phase is 24 bytes (23 usable); 14 overhead names keep 12
    char big[1024], names[400] = "";
    for (int i = 0; i < 14; i++) {
        char n[24];
        snprintf(n, sizeof n, "%s\"N%d\"", i ? "," : "", i);
        strcat(names, n);
    }
    snprintf(big, sizeof big, "{\"phase\":\"123456789012345678901234567890\",\"overhead\":[%s],\"stale\":true}", names);
    assert(astro_parse(big, strlen(big), &a));
    assert(strlen(a.phase) == 23 && a.nover == 12 && !strcmp(a.over[11], "N11") && a.stale);

    // missing fields
    assert(astro_parse("{}", 2, &a));
    assert(!a.sky && !a.rise[0] && !a.label[0] && a.illum == -1.0f && a.day0 == -1 && a.day1 == -1 && !a.nover);
    assert(astro_parse("{\"day\":[-1,-1],\"illum\":\"x\"}", 27, &a) && a.day0 == -1 && a.illum == -1.0f);

    // not an object
    assert(!astro_parse("[1]", 3, &a) && !astro_parse("nope", 4, &a) && !astro_parse("", 0, &a));

    assert(SKY_FIT_W == 357);
    fit_samples();
    fit_round();
    fit_outside();
    puts("astro_test ok");
    return 0;
}
