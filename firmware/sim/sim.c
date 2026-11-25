// PC simulator: the panel's real UI code (main/ui.c, card_*.c, takeover.c, notify.c, panel.c) on LVGL for
// Linux, with the network layer replaced by a panel document read from a file. It runs headless on a
// simulated clock, taps on-screen labels the way a finger would, and saves PPM frames of a scripted
// walk-through; sim/png.py turns them into the README's PNGs and animated tour. Layouts come from
// `deskpanel demo-layout <dir>` (default.json is served from the start). Weather comes from
// `deskpanel demo-weather <dir>` output, frames already inflated to frame-<t>.raw by sim/run.sh; without
// a weather dir the aggregator answers 404 (weather not set up) and only the Weather tab is shot.
// --layouts walks through the layouts instead, starting with none (and no weather). --rebuilt runs the
// tour after two layout changes (its frames must match the plain tour's). --no-radar serves noradar.json
// and checks the tornado takeover without a radar card. --no-wifi is the flasher page's firmware before its
// settings.txt: Wi-Fi off, no data, no layout; it checks for the set-up note.
//   deskpanel-sim [--layouts | --rebuilt | --no-radar | --no-wifi] <panel.json> <layouts dir> <outdir> [<weather dir>]
#include <math.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "astro_view.h"
#include "ellipse.h"
#include "lvgl.h"
#include "net.h"
#include "page.h"
#include "startup.h"
#include "takeover.h"
#include "ui.h"

#define W 800
#define H 480

static uint32_t fb[W * H]; // XRGB8888, rendered in place (direct mode)
static uint32_t now_ms;
static panel_t doc;
static uint32_t doc_seq = 1;
static struct { int x, y; bool down; } finger;
static net_done_t done_result;
static bool done_pending;
static const char *outdir;
static int frames;

int64_t esp_timer_get_time(void) { return (int64_t)now_ms * 1000; }
// --rebuilt: its cards are rebuilt 1 s in, so its clock runs 1 s behind; they're built in the same wall-clock
// second as the plain tour's (the demo's first due date is exactly 24 h out, on the amber threshold).
static int clock_back;
// The UI reads wall time with time(): hand it DEMO_NOW (what the aggregator's demo data was made at) plus
// the simulated elapsed time, so every run draws the same clock. (The UI uses no other wall-clock call.)
time_t time(time_t *out)
{
    const char *e = getenv("DEMO_NOW");
    time_t t = (e ? (time_t)atoll(e) : 1795032000) + now_ms / 1000 - clock_back;
    if (out) *out = t;
    return t;
}
static uint32_t tick(void) { return now_ms; }

// --- the network layer, faked: the document never goes stale, POSTs succeed and show up at once ---
static bool time_ok = true; // SNTP has set the clock
static bool wifi_unset;     // --no-wifi: WIFI_SSID empty, so nothing ever arrives
static net_state_t wifi_state = NET_UP; // --startup walks it up from NET_OFF
static bool agg_quiet;                   // --startup: the aggregator hasn't answered yet
net_status_t net_status(void) { return (net_status_t){.state = wifi_unset ? NET_OFF : wifi_state, .rssi = -48, .time_ok = time_ok}; }
bool net_wifi_unset(void) { return wifi_unset; }
int64_t net_panel_age_ms(void) { return wifi_unset || agg_quiet ? -1 : 2000; }
void net_log_heap(const char *why) { (void)why; }

bool net_panel_take(panel_t *out, uint32_t *seen)
{
    if (wifi_unset || agg_quiet || *seen == doc_seq) {
        return false;
    }
    *out = doc;
    *seen = doc_seq;
    return true;
}

bool net_mark_done(const char *card_id)
{
    for (int b = 0; b < doc.nboards; b++) {
        for (int c = 0; c < doc.boards[b].ncards; c++) {
            panel_card_t *k = &doc.boards[b].cards[c];
            if (!strcmp(k->id, card_id)) {
                k->done = true;
                strcpy(k->list, "Done");
            }
        }
    }
    snprintf(done_result.card_id, sizeof done_result.card_id, "%s", card_id);
    done_result.status = 204;
    done_pending = true;
    doc_seq++;
    return true;
}

bool net_done_result(net_done_t *out)
{
    if (!done_pending) {
        return false;
    }
    *out = done_result;
    done_pending = false;
    return true;
}

bool net_mark_read(int upto)
{
    for (int i = 0; i < doc.nnotes; i++) {
        doc.notes[i].read |= doc.notes[i].id <= upto;
    }
    doc_seq++;
    return true;
}

static const char *wxdir;
static const char *alert_pre = "alert-"; // alert-<n>.json goes with weather.json, warning-alert-<n>.json with warning.json
static int alert_n = -1;                 // a fetch waiting to be taken
static weather_t wx;
static uint32_t wx_seq;
static net_frames_t wx_frames;
static uint16_t wx_px[WX_FRAMES][800 * 368];

static size_t slurp_in(const char *dir, const char *name, void *buf, size_t cap)
{
    char path[512];
    snprintf(path, sizeof path, "%s/%s", dir, name);
    FILE *f = fopen(path, "rb");
    size_t n = f ? fread(buf, 1, cap, f) : 0;
    if (f) {
        fclose(f);
    }
    return n;
}

static size_t slurp(const char *name, void *buf, size_t cap) { return slurp_in(wxdir, name, buf, cap); }

static const char *laydir;
static layout_t lay_doc;
static uint32_t lay_seq; // 0: no layout yet (and no SD copy)
static int lay_status;   // what GET /api/v1/layout last answered: 0 not yet, 200, 404 too old (the real net.c also has 5xx: aggregator not ready, keeping what's shown)

// Serve <layouts dir>/<name> as the aggregator's layout.
static void load_layout(const char *name)
{
    static char json[16384];
    size_t n = slurp_in(laydir, name, json, sizeof json);
    if (!layout_parse(json, n, &lay_doc)) {
        fprintf(stderr, "sim: can't read a layout from %s/%s\n", laydir, name);
        exit(1);
    }
    lay_status = 200;
    lay_seq++;
}

bool net_layout_take(layout_t *out, uint32_t *seen)
{
    if (*seen == lay_seq) {
        return false;
    }
    *out = lay_doc;
    *seen = lay_seq;
    return true;
}

int net_layout_status(void) { return lay_status; }

static cards_t cards_doc;
static uint32_t cards_seq; // 0: nothing served yet
static int cards_st;       // 200 when a cards file was loaded, 404 (aggregator too old) otherwise

// Serve <layouts dir>/<name> as the aggregator's /api/v1/cards. A missing file is the 404 of an old aggregator.
static void load_cards(const char *name)
{
    static char json[20480];
    size_t n = slurp_in(laydir, name, json, sizeof json);
    if (!n || !cards_parse(json, n, &cards_doc)) {
        cards_st = 404;
        return;
    }
    cards_st = 200;
    cards_seq++;
}

bool net_cards_take(cards_t *out, uint32_t *seen)
{
    if (*seen == cards_seq) {
        return false;
    }
    *out = cards_doc;
    *seen = cards_seq;
    return true;
}

int64_t net_cards_age_ms(void) { return 2000; }

int net_cards_status(void) { return cards_st; }

// Astro: `deskpanel demo-astro` output in build/sim/astro (cwd is firmware/), skies inflated by sim/run.sh.
// load_astro("night" or "day") serves astro-<name>.json and sky-<name>.raw (change astro_doc and bump astro_seq to
// serve another document). astro_st is what GET /api/v1/astro answers (404: no location), astro_age the
// milliseconds since its last good poll.
static astro_t astro_doc;
static uint32_t astro_seq;
static int astro_st = 200;
static int64_t astro_age = 2000;
static uint16_t sky_px[SKY_W * SKY_H];
static uint32_t sky_seq;

static void load_astro(const char *name)
{
    static char json[2048];
    char file[32];
    snprintf(file, sizeof file, "astro-%s.json", name);
    size_t n = slurp_in("build/sim/astro", file, json, sizeof json);
    snprintf(file, sizeof file, "sky-%s.raw", name);
    if (!astro_parse(json, n, &astro_doc) || slurp_in("build/sim/astro", file, sky_px, sizeof sky_px) != SKY_BYTES) {
        fprintf(stderr, "sim: can't read the %s sky from build/sim/astro\n", name);
        exit(1);
    }
    astro_sky_fit(sky_px); // as net.c does with a download
    astro_seq++;
    sky_seq++;
}

bool net_astro_take(astro_t *out, uint32_t *seen)
{
    if (*seen == astro_seq) {
        return false;
    }
    *out = astro_doc;
    *seen = astro_seq;
    return true;
}

int net_astro_status(void) { return astro_st; }

int64_t net_astro_age_ms(void) { return astro_age; }

bool net_sky_take(const uint16_t **px, long long *t, uint32_t *seen) // one buffer, changed in place by load_astro
{
    if (*seen == sky_seq) {
        return false;
    }
    *px = sky_px;
    *t = astro_doc.sky;
    *seen = sky_seq;
    return true;
}

// Serve a weather document from the weather dir (weather.json; warning.json for the takeover), and the
// first time, the frames it lists.
static void load_weather(const char *name)
{
    static char json[8192];
    size_t n = slurp(name, json, sizeof json);
    if (!weather_parse(json, n, &wx)) {
        fprintf(stderr, "sim: can't read a weather document from %s/%s\n", wxdir, name);
        exit(1);
    }
    alert_pre = strcmp(name, "warning.json") ? "alert-" : "warning-alert-";
    wx_seq++;
    if (wx_frames.n) {
        return; // the demo's documents all list the same frames
    }
    for (int i = 0; i < wx.nframes; i++) {
        char raw[40];
        snprintf(raw, sizeof raw, "frame-%lld.raw", wx.frames[i]);
        if (slurp(raw, wx_px[i], sizeof wx_px[i]) != sizeof wx_px[i]) {
            fprintf(stderr, "sim: %s/%s is missing or short\n", wxdir, raw);
            exit(1);
        }
        wx_frames.t[i] = wx.frames[i];
        wx_frames.px[i] = wx_px[i];
        wx_frames.n = i + 1;
    }
}

bool net_weather_take(weather_t *out, uint32_t *seen)
{
    if (*seen == wx_seq) {
        return false;
    }
    *out = wx;
    *seen = wx_seq;
    return true;
}

int net_weather_status(void) { return wxdir ? 200 : 404; }

bool net_frames_take(net_frames_t *out, uint32_t *seen)
{
    if (*seen == 1 || !wx_frames.n) {
        return false;
    }
    *out = wx_frames;
    *seen = 1;
    return true;
}

bool net_fetch_alert(int n)
{
    alert_n = n;
    return true;
}

bool net_alert_take(int *n, int *status, char *buf, size_t cap) // answered at once, from the weather dir
{
    if (alert_n < 0) {
        return false;
    }
    char name[40];
    snprintf(name, sizeof name, "%s%d.json", alert_pre, alert_n);
    size_t len = slurp(name, buf, cap - 1);
    buf[len] = '\0';
    *n = alert_n;
    *status = len ? 200 : 404;
    alert_n = -1;
    return true;
}

// --- display, touch, clock ---
static void flush(lv_display_t *d, const lv_area_t *area, uint8_t *px) { lv_display_flush_ready(d); }

static void read_finger(lv_indev_t *indev, lv_indev_data_t *data)
{
    data->point.x = finger.x;
    data->point.y = finger.y;
    data->state = finger.down ? LV_INDEV_STATE_PRESSED : LV_INDEV_STATE_RELEASED;
}

static void run(int ms)
{
    for (int t = 0; t < ms; t += 5) {
        now_ms += 5;
        lv_timer_handler();
    }
}

// The first visible label under `o` whose text equals `text` (or starts with it, if prefix).
static lv_obj_t *find(lv_obj_t *o, const char *text, bool prefix)
{
    if (lv_obj_check_type(o, &lv_label_class) && lv_obj_is_visible(o)) {
        const char *t = lv_label_get_text(o);
        if (prefix ? !strncmp(t, text, strlen(text)) : !strcmp(t, text)) {
            return o;
        }
    }
    for (uint32_t i = 0; i < lv_obj_get_child_count(o); i++) {
        lv_obj_t *f = find(lv_obj_get_child(o, (int32_t)i), text, prefix);
        if (f) {
            return f;
        }
    }
    return NULL;
}

// The first visible object of that class under `o` (depth first), or NULL.
static lv_obj_t *find_class(lv_obj_t *o, const lv_obj_class_t *cls)
{
    if (lv_obj_check_type(o, cls) && lv_obj_is_visible(o)) {
        return o;
    }
    for (uint32_t i = 0; i < lv_obj_get_child_count(o); i++) {
        lv_obj_t *f = find_class(lv_obj_get_child(o, (int32_t)i), cls);
        if (f) {
            return f;
        }
    }
    return NULL;
}

// The first shown object under `o` that pokes out of the area `in`, or NULL.
static lv_obj_t *poking_out(lv_obj_t *o, const lv_area_t *in)
{
    for (uint32_t i = 0; i < lv_obj_get_child_count(o); i++) {
        lv_obj_t *c = lv_obj_get_child(o, (int32_t)i), *f;
        lv_area_t a;
        lv_obj_get_coords(c, &a);
        if (lv_obj_is_hidden(c)) {
            continue;
        }
        if (a.x1 < in->x1 || a.y1 < in->y1 || a.x2 > in->x2 || a.y2 > in->y2) {
            return c;
        }
        if ((f = poking_out(c, in))) {
            return f;
        }
    }
    return NULL;
}

// The tab on screen: its children are its cards, in layout order.
static lv_obj_t *shown_tab(lv_obj_t *tv)
{
    return lv_obj_get_child(lv_tabview_get_content(tv), (int32_t)lv_tabview_get_tab_active(tv));
}

// Nothing in a card on the tab on screen is cut off: everything stays inside the card's padding.
static bool cards_fit(lv_obj_t *tv)
{
    lv_obj_t *tab = shown_tab(tv);
    for (uint32_t i = 0; i < lv_obj_get_child_count(tab); i++) {
        lv_obj_t *card = lv_obj_get_child(tab, (int32_t)i);
        lv_area_t in, a;
        lv_obj_get_content_coords(card, &in);
        lv_obj_t *o = poking_out(card, &in);
        if (o) {
            lv_obj_get_coords(o, &a);
            fprintf(stderr, "sim: card %u on the tab: %d,%d-%d,%d is outside %d,%d-%d,%d\n", (unsigned)i, (int)a.x1,
                    (int)a.y1, (int)a.x2, (int)a.y2, (int)in.x1, (int)in.y1, (int)in.x2, (int)in.y2);
            return false;
        }
    }
    return true;
}

// Every gauge on the tab on screen keeps its number clear of its ring (round on the glass: ellipse_w() wide).
static bool gauges_clear(lv_obj_t *tv)
{
    lv_obj_t *tab = shown_tab(tv);
    for (uint32_t i = 0; i < lv_obj_get_child_count(tab); i++) {
        lv_obj_t *arc = find_class(lv_obj_get_child(tab, (int32_t)i), &lv_arc_class);
        int room = arc ? ellipse_w(lv_obj_get_width(arc)) - 2 * lv_obj_get_style_arc_width(arc, LV_PART_MAIN) - 2 : 0;
        if (arc && lv_obj_get_width(lv_obj_get_child(arc, 0)) > room) {
            fprintf(stderr, "sim: the gauge of card %u on the tab touches its ring (%d px in %d)\n", (unsigned)i,
                    (int)lv_obj_get_width(lv_obj_get_child(arc, 0)), room);
            return false;
        }
    }
    return true;
}

static bool differs(uint32_t a, uint32_t b) // by more than 12 in a channel
{
    for (int s = 0; s < 24; s += 8) {
        if (abs((int)(a >> s & 0xff) - (int)(b >> s & 0xff)) > 12) {
            return true;
        }
    }
    return false;
}

// Every opaque gauge on the tab on screen is round on the glass: across its middle rows ellipse_w(h) wide (± 1), and
// its top at its box's (± 1).
static bool gauges_round(lv_obj_t *tv)
{
    lv_refr_now(NULL);
    lv_obj_t *tab = shown_tab(tv);
    for (uint32_t i = 0; i < lv_obj_get_child_count(tab); i++) {
        lv_obj_t *arc = find_class(lv_obj_get_child(tab, (int32_t)i), &lv_arc_class);
        if (!arc || lv_obj_get_style_opa_recursive(arc, LV_PART_MAIN) < LV_OPA_MAX) {
            continue;
        }
        lv_area_t a;
        lv_obj_get_coords(arc, &a);
        int h = lv_area_get_height(&a), cx = a.x1 + h / 2, cy = a.y1 + h / 2, want = ellipse_w(h);
        int x0 = W, x1 = -1, top = H;
        uint32_t bg = fb[a.y1 * W + a.x1];
        for (int y = cy - 2; y <= cy + 2; y++) {
            for (int x = a.x1; x <= a.x2; x++) {
                if (differs(fb[y * W + x], bg)) {
                    x0 = LV_MIN(x0, x);
                    x1 = LV_MAX(x1, x);
                }
            }
        }
        for (int y = a.y1 - 1; y <= cy && top == H; y++) {
            top = differs(fb[y * W + cx], bg) ? y : H;
        }
        if (abs(x1 - x0 + 1 - want) > 1 || abs(top - (int)a.y1) > 1) {
            fprintf(stderr, "sim: the gauge of card %u is %d px wide, want %d \xc2\xb1 1 (top %d, box %d)\n",
                    (unsigned)i, x1 - x0 + 1, want, top, (int)a.y1);
            return false;
        }
    }
    return true;
}

// Along the middle of the gauge's band, in 1° steps 8° clear of every end, the track and the value each keep one
// colour (± 4 a channel): greyed, a part blended twice where its arcs overlap would show.
static bool gauge_even(lv_obj_t *arc)
{
    lv_refr_now(NULL);
    lv_area_t a;
    lv_obj_get_coords(arc, &a);
    int h = lv_area_get_height(&a);
    float r = h / 2.0f - lv_obj_get_style_arc_width(arc, LV_PART_MAIN) / 2.0f, cx = a.x1 + h / 2, cy = a.y1 + h / 2;
    int rot = lv_arc_get_rotation(arc) + 90, a0 = lv_arc_get_bg_angle_start(arc) + rot; // degrees from up
    int bg = lv_arc_get_bg_angle_end(arc) - lv_arc_get_bg_angle_start(arc);
    int val = lv_arc_get_angle_end(arc) - lv_arc_get_angle_start(arc);
    int v = lv_arc_get_angle_start(arc) + rot + (val % 360 + 360) % 360;
    int ends[2][2] = {{a0, v}, {v, a0 + (bg % 360 + 360) % 360}}; // the value, then the track past it
    for (int p = 0; p < 2; p++) {
        int lo[3] = {255, 255, 255}, hi[3] = {0, 0, 0};
        for (int d = ends[p][0] + 8; d <= ends[p][1] - 8; d++) {
            float t = d * (float)M_PI / 180;
            uint32_t px = fb[lroundf(cy - r * cosf(t)) * W + lroundf(cx + r * sinf(t) / PANEL_ASPECT)];
            for (int c = 0; c < 3; c++) {
                lo[c] = LV_MIN(lo[c], (int)(px >> (8 * c) & 0xff));
                hi[c] = LV_MAX(hi[c], (int)(px >> (8 * c) & 0xff));
            }
        }
        for (int c = 0; c < 3; c++) {
            if (hi[c] - lo[c] > 4) {
                fprintf(stderr, "sim: the gauge's %s varies by %d along its band\n", p ? "track" : "value", hi[c] - lo[c]);
                return false;
            }
        }
    }
    return true;
}

// A full redraw changes nothing: the redraws so far left no pixel a full one doesn't draw.
static bool redraw_clean(void)
{
    static uint32_t before[W * H];
    lv_refr_now(NULL);
    memcpy(before, fb, sizeof fb);
    lv_obj_invalidate(lv_screen_active());
    lv_refr_now(NULL);
    return !memcmp(before, fb, sizeof fb);
}

// The card (a child of the tab on screen) holding the visible label `text`, or NULL.
static lv_obj_t *card_with(lv_obj_t *tv, const char *text)
{
    lv_obj_t *tab = shown_tab(tv);
    lv_obj_t *o = find(tab, text, false);
    while (o && lv_obj_get_parent(o) != tab) {
        o = lv_obj_get_parent(o);
    }
    return o;
}

static void press(int x, int y)
{
    finger.x = x;
    finger.y = y;
    finger.down = true;
    run(100);
    finger.down = false;
    run(700);
}

static void tap_at(const char *text, bool prefix)
{
    lv_obj_t *o = find(lv_layer_top(), text, prefix); // sheets and pop-ups first
    if (!o) {
        o = find(lv_screen_active(), text, prefix);
    }
    if (!o) {
        fprintf(stderr, "sim: nothing labelled \"%s\" on screen\n", text);
        exit(1);
    }
    lv_area_t a;
    lv_obj_get_coords(o, &a);
    press((a.x1 + a.x2) / 2, (a.y1 + a.y2) / 2);
}

static void tap(const char *text) { tap_at(text, false); }

static void shot(const char *name)
{
    lv_refr_now(NULL);
    char path[512];
    snprintf(path, sizeof path, "%s/%02d-%s.ppm", outdir, frames++, name);
    FILE *f = fopen(path, "wb");
    if (!f) {
        perror(path);
        exit(1);
    }
    fprintf(f, "P6\n%d %d\n255\n", W, H);
    for (int i = 0; i < W * H; i++) {
        uint8_t rgb[3] = {(uint8_t)(fb[i] >> 16), (uint8_t)(fb[i] >> 8), (uint8_t)fb[i]};
        fwrite(rgb, 1, 3, f);
    }
    fclose(f);
    fprintf(stderr, "sim: %s\n", path);
}

static int fail(const char *what)
{
    fprintf(stderr, "sim: %s\n", what);
    return 1;
}

static int gpu_fan(lv_obj_t *tv, const panel_t *demo);

// The layouts walk-through: the messages before there's a layout, then the demo layouts on the Homelab tab,
// then a layout change while a sheet is open.
static int layouts(void)
{
    tap("OK"); // the newest note's pop-up
    shot("layout-none"); // no answer from /api/v1/layout yet, and no SD copy
    lay_status = 404;
    run(600);
    shot("layout-404");
    load_layout("default.json");
    run(600);
    shot("homelab-grid");
    load_layout("gpu1.json");
    run(600);
    shot("homelab-gpu1");
    panel_gpu_t two[PANEL_MAX_GPUS]; // gpu4.json names hosts a..d: give the demo's two GPUs those names
    memcpy(two, doc.gpus, sizeof two);
    for (int i = 0; i < 4; i++) {
        doc.gpus[i] = two[i & 1];
        snprintf(doc.gpus[i].host, sizeof doc.gpus[i].host, "%c", 'a' + i);
    }
    doc.ngpus = 4;
    doc_seq++;
    load_layout("gpu4.json");
    run(600);
    shot("homelab-gpu4");
    memcpy(doc.gpus, two, sizeof two);
    doc.ngpus = 2;
    doc_seq++;
    load_layout("gpu0.json");
    run(600);
    shot("homelab-gpu0");
    doc.svcs[doc.nsvcs - 1].on = false; // a service hidden behind "+N" goes down: the "+N" turns red
    doc_seq++;
    run(600);
    lv_obj_t *more = find(lv_screen_active(), "+2", false);
    if (!more || !lv_color_eq(lv_obj_get_style_text_color(more, 0), lv_color_hex(COL_BAD))) {
        return fail("a down service behind \"+2\" didn't turn it red");
    }
    doc.svcs[doc.nsvcs - 1].on = true;
    doc_seq++;
    load_layout("custom.json"); // moved and resized cards, and a pool card for "tnak", which doesn't exist
    run(600);
    shot("layout-unknown-src");
    if (!find(lv_screen_active(), "no pool 'tnak'", false)) {
        return fail("the 'tnak' pool card doesn't say \"no pool 'tnak'\"");
    }

    // A new layout while a sheet is open: the rebuild closes it, and the new cards answer taps.
    load_layout("default.json");
    run(600);
    tap("NAS ALERTS");
    if (!ui_sheet_is_open()) {
        return fail("the NAS alerts card didn't open its sheet");
    }
    load_layout("gpu1.json");
    run(1000);
    if (ui_sheet_is_open() || find(lv_layer_top(), "NAS alerts", false)) {
        return fail("the sheet is still open after a layout change");
    }
    tap("workstation"); // the new GPU card
    tap("NAS ALERTS");
    shot("layout-rebuild");
    if (!ui_sheet_is_open()) {
        return fail("the new NAS alerts card didn't open its sheet");
    }

    // Every Homelab card at every designed size, on two tabs, nothing cut off. The 2x3 GPU card's tap opens
    // the second: tapped on its bar, it must still get there (a clickable bar would swallow the tap).
    load_layout("sizes.json");
    run(600);
    shot("sizes");
    lv_obj_t *bar = find_class(lv_screen_active(), &lv_bar_class), *tv = find_class(lv_screen_active(), &lv_tabview_class);
    if (!bar || !tv) {
        return fail("no bar or no tabview on the sizes tab");
    }
    if (!cards_fit(tv)) {
        return fail("a card on the sizes tab doesn't fit its contents");
    }
    lv_area_t a;
    lv_obj_get_coords(bar, &a);
    press((a.x1 + a.x2) / 2, (a.y1 + a.y2) / 2);
    run(600);
    if (lv_tabview_get_tab_active(tv) != 1) {
        return fail("a tap on the GPU card's bar didn't reach the card");
    }
    shot("sizes-2");
    if (!cards_fit(tv)) {
        return fail("a card on the second sizes tab doesn't fit its contents");
    }
    // No services and 16 peers: the 4x2 dots card's one group wraps onto its second line and ends in "+N".
    static panel_t demo; // the demo data, for what follows
    demo = doc;
    static const char *const names[] = {"desktop-tower", "kitchen-tablet", "living-room-tv", "office-printer", "router",
                                        "doorbell-camera", "watch", "kiosk", "nas-2", "car"};
    for (int i = 0; i < (int)(sizeof names / sizeof names[0]) && doc.npeers < PANEL_MAX_PEERS; i++) {
        panel_peer_t *pe = &doc.peers[doc.npeers++];
        snprintf(pe->name, sizeof pe->name, "%s", names[i]);
        pe->on = i % 4 != 2;
    }
    doc.nsvcs = 0;
    doc_seq++;
    run(600);
    shot("sizes-wrap");
    if (!cards_fit(tv)) {
        return fail("a card doesn't fit its contents with 16 peers");
    }

    // Names as long as a real setup's. The layout names them before the data does, so its cards say "no pool
    // 'archive-4tb-02'" (and with one peer and one service the 2x1 dots card keeps both titles); then the
    // data catches up; then the default layout. Nothing may poke out of a card.
    doc = demo;
    doc.peers[0] = doc.peers[1]; // "nas"
    doc.svcs[0] = doc.svcs[2];   // "pihole"
    doc.npeers = doc.nsvcs = 1;
    doc_seq++;
    load_layout("sizes-longnames.json");
    run(600);
    ui_show_tab("Sizes");
    run(600);
    shot("sizes-longnames-missing");
    if (!cards_fit(tv)) {
        return fail("a card saying its long source is missing doesn't fit");
    }
    doc = demo;
    snprintf(doc.gpus[0].host, sizeof doc.gpus[0].host, "studio");
    snprintf(doc.gpus[0].name, sizeof doc.gpus[0].name, "RTX 5060 Ti");
    snprintf(doc.gpus[1].host, sizeof doc.gpus[1].host, "storage");
    snprintf(doc.gpus[1].name, sizeof doc.gpus[1].name, "RTX 2070 SUPER");
    snprintf(doc.pools[0].name, sizeof doc.pools[0].name, "media-vault1");
    snprintf(doc.pools[1].name, sizeof doc.pools[1].name, "archive-4tb-02");
    doc_seq++;
    run(600);
    shot("sizes-longnames");
    if (!cards_fit(tv)) {
        return fail("a card on the sizes tab doesn't fit long names");
    }
    ui_show_tab("Sizes 2");
    run(600);
    shot("sizes-longnames-2");
    if (!cards_fit(tv)) {
        return fail("a card on the second sizes tab doesn't fit long names");
    }
    load_layout("longnames.json");
    run(600);
    shot("homelab-longnames");
    if (!cards_fit(tv)) {
        return fail("a Homelab card doesn't fit long names");
    }

    // General cards fed by cards.json (served from the start): every type at every designed size, a tab per
    // shot, then the longest title, value and reason on the small sizes. Nothing may poke out of a card.
    load_layout("general-sizes.json");
    run(600);
    for (int i = 0; i < lay_doc.ntabs; i++) {
        char name[24];
        snprintf(name, sizeof name, i ? "general-sizes-%d" : "general-sizes", i + 1);
        ui_show_tab(lay_doc.tabs[i].name);
        run(600);
        shot(name);
        if (!cards_fit(tv)) {
            return fail("a general card doesn't fit its contents");
        }
        if (!gauges_round(tv)) {
            return fail("a gauge isn't round on the glass");
        }
        if (!strncmp(lay_doc.tabs[i].name, "Gen graphs", 10) && !find_class(shown_tab(tv), &lv_chart_class)) {
            return fail("a graph card draws no graph");
        }
    }
    ui_show_tab(lay_doc.tabs[0].name);
    run(600);
    for (int i = 0; i < cards_doc.n; i++) { // the 1-column cards lose their entries: they say "—"; the 1x2
        card_val_t *e = &cards_doc.c[i];      // gauge is "none" with a reason instead, shown in its title row
        if (!strcmp(e->id, "gs-gauge-1x2")) {
            e->st = CV_NONE;
            snprintf(e->sub, sizeof e->sub, "set PROMETHEUS_URL");
        } else if (strstr(e->id, "-1x")) {
            e->id[0] = '_';
        }
    }
    cards_seq++;
    run(600);
    shot("general-none");
    lv_obj_t *load = card_with(tv, "Load");
    if (!load || !find(load, "\xe2\x80\x94", false)) {
        return fail("a general card with no entry in the cards document doesn't say \"\xe2\x80\x94\"");
    }
    if (!find(lv_screen_active(), "set P", true)) {
        return fail("a gauge with no value doesn't show the aggregator's reason");
    }
    // Stale, warn and bad: a greyed card, amber and red edges and values.
    load_cards("cards.json");
    static const struct { const char *id; cv_state_t st; } states[] = {
        {"gs-num-1x2", CV_STALE}, {"gs-bar-1x2", CV_WARN}, {"gs-gauge-2x3", CV_WARN},
        {"gs-gauge-1x2", CV_BAD}, {"gs-num-1x1", CV_BAD}};
    for (size_t i = 0; i < sizeof states / sizeof states[0]; i++) {
        card_val_t *e = (card_val_t *)cards_find(&cards_doc, states[i].id);
        if (!e) {
            return fail("cards.json lacks a general-sizes card");
        }
        e->st = states[i].st;
    }
    cards_seq++;
    run(600);
    shot("general-states");
    lv_obj_t *stale = card_with(tv, "Load"), *warn = card_with(tv, "Disk /");
    if (!stale || lv_obj_get_style_opa(stale, 0) != LV_OPA_60 || !warn ||
        !lv_color_eq(lv_obj_get_style_border_color(warn, 0), lv_color_hex(COL_WARN))) {
        return fail("a stale general card isn't greyed, or a warn one has no amber edge");
    }
    // Greyed, a gauge blends each part once: no lighter seams where its arcs overlap. (No shot: the next step's
    // load_cards puts it back.)
    ((card_val_t *)cards_find(&cards_doc, "gs-gauge-1x2"))->st = CV_STALE;
    cards_seq++;
    run(600);
    lv_obj_t *grey = card_with(tv, "Temp"), *ring = grey ? find_class(grey, &lv_arc_class) : NULL;
    if (!ring || lv_obj_get_style_opa(grey, 0) != LV_OPA_60 || !gauge_even(ring)) {
        return fail("a greyed gauge isn't even along its band");
    }
    // A redraw of part of it (its left middle: the band and some of its number) matches a full one: its layers are
    // only as big as the redraw, and the arcs stay centred on the gauge.
    lv_area_t part;
    lv_obj_get_coords(ring, &part);
    int ph = lv_obj_get_height(ring);
    part = (lv_area_t){part.x1, part.y1 + ph / 4, part.x1 + ph / 2, part.y1 + 3 * ph / 4};
    lv_obj_invalidate_area(ring, &part);
    if (!redraw_clean()) {
        return fail("a partial redraw of a greyed gauge differs from a full one");
    }
    // Graphs short of an hour: CPU has its newest 10 points, drawn in the right 10/60 of its plot; Network has no
    // value (its reason, an empty plot); Temp is stale.
    load_cards("cards.json");
    ui_show_tab("Gen graphs");
    for (int i = 0; i < cards_doc.n; i++) {
        card_val_t *e = &cards_doc.c[i];
        size_t n = strlen(e->g[0]);
        if (!strcmp(e->id, "gs-graph-4x3") && n >= 10) {
            memmove(e->g[0], e->g[0] + n - 10, 11);
        } else if (!strcmp(e->id, "gs-graph-2x2")) {
            e->st = CV_NONE;
            e->v[0] = e->v2[0] = '\0';
            e->ng = 0;
            snprintf(e->sub, sizeof e->sub, "no data");
        } else if (!strcmp(e->id, "gs-graph-2x3")) {
            e->st = CV_STALE;
        }
    }
    cards_seq++;
    run(600);
    shot("general-graph-short");
    lv_obj_t *cpu = card_with(tv, "CPU"), *plot = cpu ? find_class(cpu, &lv_chart_class) : NULL;
    if (!plot) {
        return fail("the CPU graph card draws no graph");
    }
    lv_area_t pa;
    lv_obj_get_coords(plot, &pa);
    int x0 = W, x1 = -1, pw = lv_area_get_width(&pa); // line 1's (accent blue) pixels in the plot
    for (int y = pa.y1; y <= pa.y2; y++) {
        for (int x = pa.x1; x <= pa.x2; x++) {
            uint32_t p = fb[y * W + x];
            if ((int)(p & 0xff) - (int)((p >> 16) & 0xff) > 60) {
                x0 = x < x0 ? x : x0;
                x1 = x > x1 ? x : x1;
            }
        }
    }
    if (x0 < pa.x1 + pw * 50 / 60 - 2 || x0 > pa.x1 + pw * 52 / 60 || x1 < pa.x2 - 4) {
        fprintf(stderr, "sim: line 1 spans x %d..%d in the plot %d..%d\n", x0, x1, (int)pa.x1, (int)pa.x2);
        return fail("a 10-point series isn't drawn in the right 10/60 of its graph");
    }
    if (!find(card_with(tv, "Network"), "no data", false)) {
        return fail("a graph with no value doesn't show the aggregator's reason");
    }
    if (!cards_fit(tv)) {
        return fail("a graph card short of an hour doesn't fit its contents");
    }
    // Two values at 2x3 (C9): the Temp card becomes a second network graph, "Uplink"; its values sit 20 px apart.
    load_cards("cards.json");
    card_val_t *pair = (card_val_t *)cards_find(&cards_doc, "gs-graph-2x3");
    const card_val_t *net = cards_find(&cards_doc, "gs-graph-2x2");
    layout_card_t *temp = &lay_doc.tabs[1].cards[2];
    if (!pair || !net || strcmp(temp->id, "gs-graph-2x3")) {
        return fail("cards.json or general-sizes.json lacks a graph card");
    }
    *pair = *net;
    snprintf(pair->id, sizeof pair->id, "gs-graph-2x3");
    cards_seq++;
    snprintf(temp->title, sizeof temp->title, "Uplink");
    lay_seq++;
    run(600);
    shot("general-graph-two");
    lv_obj_t *up = card_with(tv, "Uplink"), *dn = up ? find(up, "\xe2\x86\x93", true) : NULL; // ↓, then ↑
    up = up ? find(up, "\xe2\x86\x91", true) : NULL;
    lv_area_t da, ua;
    if (!dn || !up) {
        return fail("a 2x3 graph doesn't show its two values");
    }
    lv_obj_get_coords(dn, &da);
    lv_obj_get_coords(up, &ua);
    if (ua.x1 - da.x2 < 20) {
        return fail("a 2x3 graph's two values sit less than 20 px apart");
    }
    if (!cards_fit(tv)) {
        return fail("a 2x3 graph with two values doesn't fit its contents");
    }
    load_cards("cards.json");
    for (int i = 0; i < cards_doc.n; i++) { // Ruling C8: the 1x2 number and bar carry a 23-byte sub-line too, and
        card_val_t *e = &cards_doc.c[i];      // the none cards (gl-many) a 23-byte reason
        if (!strcmp(e->id, "gl-num-1x2") || !strcmp(e->id, "gl-bar-1x2") || !strncmp(e->id, "gl-many", 7)) {
            memset(e->sub, 'w', 23);
            e->sub[23] = '\0';
        }
    }
    cards_seq++;
    load_layout("general-long.json");
    run(600);
    shot("general-long");
    if (!cards_fit(tv)) {
        return fail("a general card doesn't fit long text");
    }
    // ...and at a graph size (C9): a 2x2 graph with two 15-byte values beside the 27-byte title leaves the title
    // some room (its "...", at least).
    layout_tab_t *gl = &lay_doc.tabs[0];
    layout_card_t *k = &gl->cards[gl->ncards++];
    *k = gl->cards[0];
    snprintf(k->id, sizeof k->id, "gs-graph-2x2");
    snprintf(k->type, sizeof k->type, "graph");
    k->col = 1, k->row = 5, k->w = k->h = 2;
    card_val_t *lv = (card_val_t *)cards_find(&cards_doc, "gs-graph-2x2");
    if (!lv) {
        return fail("cards.json lacks gs-graph-2x2");
    }
    memset(lv->v, 'w', 15), lv->v[15] = '\0';
    memcpy(lv->v2, lv->v, sizeof lv->v);
    cards_seq++;
    lay_seq++;
    run(600);
    shot("general-long-graph");
    lv_obj_t *title = lv_obj_get_child(lv_obj_get_child(lv_obj_get_child(shown_tab(tv), gl->ncards - 1), 0), 0);
    if (!cards_fit(tv) || lv_obj_get_width(title) < 16) {
        return fail("a 2x2 graph with two long values doesn't fit, or leaves its title no room");
    }
    // The approved mockup: the "server" tab beside the default ones.
    load_layout("server.json");
    run(600);
    ui_show_tab("server");
    run(600);
    shot("server-tab");
    cpu = card_with(tv, "server CPU");
    if (!cpu || !find_class(cpu, &lv_chart_class)) {
        return fail("the server CPU card draws no graph");
    }
    if (!cards_fit(tv)) {
        return fail("a card on the server tab doesn't fit its contents");
    }

    // JSON source (Stage 3): an app's status JSON as a number with a unit, a graph, a status from a string ("running"),
    // a gauge and a bar. The demo answers any json: key with a wave and the "status" path with the word.
    load_cards("cards.json");
    load_layout("json-demo.json");
    run(600);
    shot("json-demo");
    lv_obj_t *js = card_with(tv, "App"), *jq = card_with(tv, "Queue");
    const card_val_t *jv = cards_find(&cards_doc, "js-queue");
    const card_val_t *ja = cards_find(&cards_doc, "js-app");
    if (!js || !ja || strcmp(ja->v, "up") || ja->st != CV_OK || !jq || !jv || !strstr(jv->v, "jobs") || !find(jq, jv->v, false) ||
        !find_class(card_with(tv, "Latency"), &lv_chart_class) || !cards_fit(tv)) {
        return fail("the JSON demo doesn't show a number with its unit, a graph and a status from a string");
    }

    // GPU slots (S2): today's card beside VRAM as a graph, power as text over temp and procs, and a 2x2 util graph
    // (line 2 gives it its room). Nothing may poke out of a card.
    doc = demo; // the hosts' own names again
    doc_seq++;
    load_layout("gpu-slots.json");
    run(600);
    shot("gpu-slots");
    static const struct { bool bar, chart; const char *line1; } want[] = {
        {true, false, "VRAM  "}, {false, true, "VRAM  "}, {false, false, "Power "}, {false, true, "Util "}};
    lv_obj_t *tab = shown_tab(tv);
    for (int i = 0; i < 4; i++) {
        lv_obj_t *c = lv_obj_get_child(tab, i);
        if (!c || !find_class(c, &lv_bar_class) != !want[i].bar || !find_class(c, &lv_chart_class) != !want[i].chart ||
            !find(c, want[i].line1, true)) {
            fprintf(stderr, "sim: GPU card %d on the GPU slots tab\n", i);
            return fail("a GPU card doesn't draw its slots");
        }
    }
    char l2[48];
    snprintf(l2, sizeof l2, "%d°  " LV_SYMBOL_BULLET "  %d proc", doc.gpus[1].temp, doc.gpus[1].procs); // server: 1
    if (!find(lv_obj_get_child(tab, 2), l2, false)) {
        return fail("line 2 doesn't read temp and procs");
    }
    snprintf(l2, sizeof l2, "%d%%  " LV_SYMBOL_BULLET "  %d W", doc.gpus[0].util, doc.gpus[0].power);
    if (!find(lv_obj_get_child(tab, 0), l2, false) || find(lv_obj_get_child(tab, 3), l2, false)) {
        return fail("the default GPU card lost its line 2, or the 2x2 graph kept it");
    }
    if (!cards_fit(tv)) {
        return fail("a GPU card with slots doesn't fit its contents");
    }
    // Every other size, some slots falling back: a 1x3 temp graph (its gauge shrinks for it); a 1x2's graph slot
    // draws a bar, or power (no bar) its text; a 1x1, two 1x3s and a 2x2 with other gauges (one 1x3's small, over
    // the server's temp graph), a 2x3 VRAM graph with no line 2. The server's GPU at 100 % util: the widest gauge text.
    static const struct { int col, row, w, h; const char *id, *src, *gauge, *line1, *as, *line2[2]; } extra[] = {
        {2, 1, 1, 2, "gp-1x2-graph", "server", "temp", "vram", "graph", {"util", "power"}},
        {2, 3, 1, 2, "gp-1x2-power", "server", "temp", "power", "graph", {"util", "power"}},
        {1, 4, 1, 1, "gp-1x1-util", "server", "util", "vram", "bar", {"util", "power"}},
        {4, 1, 1, 3, "gp-1x3-util", "server", "util", "util", "bar", {""}},
        {3, 1, 1, 3, "gp-1x3-graph", "server", "util", "temp", "graph", {""}},
        {1, 5, 2, 2, "gp-2x2-text", "server", "vram", "temp", "text", {"procs"}},
        {3, 4, 2, 3, "gp-vram-graph", "workstation", "vram", "vram", "graph", {""}}};
    layout_tab_t *t2 = &lay_doc.tabs[1];
    for (size_t i = 0; i < sizeof extra / sizeof extra[0]; i++) {
        layout_card_t *m = &t2->cards[t2->ncards++];
        *m = t2->cards[0]; // the 1x3 temp graph
        m->col = extra[i].col, m->row = extra[i].row, m->w = extra[i].w, m->h = extra[i].h;
        snprintf(m->id, sizeof m->id, "%s", extra[i].id);
        snprintf(m->src, sizeof m->src, "%s", extra[i].src);
        snprintf(m->gauge, sizeof m->gauge, "%s", extra[i].gauge);
        snprintf(m->line1, sizeof m->line1, "%s", extra[i].line1);
        snprintf(m->as, sizeof m->as, "%s", extra[i].as);
        for (int j = 0; j < 2; j++) {
            snprintf(m->line2[j], sizeof m->line2[j], "%s", extra[i].line2[j] ? extra[i].line2[j] : "");
        }
    }
    lay_seq++;
    doc.gpus[1].util = 100;
    doc_seq++;
    run(600);
    ui_show_tab("GPU slots 2");
    run(600);
    shot("gpu-slots-2");
    tab = shown_tab(tv);
    lv_obj_t *g13 = lv_obj_get_child(tab, 0), *g12 = lv_obj_get_child(tab, 1), *gpw = lv_obj_get_child(tab, 2);
    if (!find_class(g13, &lv_chart_class) || !find_class(g13, &lv_arc_class) || !find(g13, "Temp ", true) ||
        !find_class(g12, &lv_bar_class) || find_class(g12, &lv_chart_class) ||
        find_class(gpw, &lv_bar_class) || find_class(gpw, &lv_chart_class) || !find(gpw, "Power ", true)) {
        return fail("a GPU slot doesn't fall back where it doesn't fit");
    }
    if (!cards_fit(tv)) {
        return fail("a GPU card with slots doesn't fit its contents at a smaller size");
    }
    if (!gauges_clear(tv) || !gauges_round(tv)) {
        return fail("a gauge's number touches its ring");
    }
    // A graph slot with no history yet ("st":"none"): the 1x3 keeps its text, over an empty plot, never "—".
    card_val_t *e = (card_val_t *)cards_find(&cards_doc, "gp-1x3-graph");
    if (!e) {
        return fail("cards.json lacks gp-1x3-graph");
    }
    e->st = CV_NONE;
    e->ng = 0;
    cards_seq++;
    run(600);
    if (!find(g13, "Temp ", true) || !find_class(g13, &lv_chart_class) || find(g13, "\xe2\x80\x94", false)) {
        return fail("a GPU graph with no history yet doesn't keep its text over an empty plot");
    }
    // Its history goes stale: the plot keeps its points, greyed; the text (from /panel) doesn't grey.
    load_cards("cards.json");
    if (!(e = (card_val_t *)cards_find(&cards_doc, "gp-1x3-graph"))) {
        return fail("cards.json lacks gp-1x3-graph");
    }
    e->st = CV_STALE;
    run(600);
    if (lv_obj_get_style_opa(find_class(g13, &lv_chart_class), 0) != LV_OPA_60 ||
        lv_obj_get_style_opa(g13, 0) != LV_OPA_COVER) {
        return fail("a GPU graph with stale history isn't greyed, or greys the whole card");
    }
    // The agent goes offline: line 1 reads "--", not a false zero.
    doc.gpus[1].up = false;
    doc_seq++;
    run(600);
    if (!find(gpw, "Power --", false) || !find(g13, "Temp --", false)) {
        return fail("an offline GPU's line 1 shows a reading");
    }
    return gpu_fan(tv, &demo);
}

// The fan's entries in the cards document (ids gf-*): fan text v and fraction f (-1: unknown); an old agent also has
// no fan history, so its fan graphs have no points.
static void fan_entries(const char *v, float f, bool old_agent)
{
    for (int i = 0; i < cards_doc.n; i++) {
        card_val_t *e = &cards_doc.c[i];
        if (strncmp(e->id, "gf-", 3)) {
            continue;
        }
        snprintf(e->v, sizeof e->v, "%s", v);
        e->f = f;
        for (int t = 0; t < lay_doc.ntabs; t++) {
            for (int k = 0; k < lay_doc.tabs[t].ncards; k++) {
                const layout_card_t *lc = &lay_doc.tabs[t].cards[k];
                if (old_agent && !strcmp(lc->id, e->id) && !strcmp(lc->line1, "fan") && !strcmp(lc->as, "graph")) {
                    e->st = CV_NONE, e->ng = 0;
                }
            }
        }
    }
    cards_seq++;
}

// GPU fan (J7), the fan in every slot at every size: the workstation's spinning, the server's at 0 % (zero-RPM: a
// reading, not unknown), and nas, a passively cooled GPU with no reading ("--"). The demo has two GPUs: nas joins
// the panel document as a copy of the server's. Then the widest number (100 %) and an old agent (no fan at all).
static int gpu_fan(lv_obj_t *tv, const panel_t *demo)
{
    doc = *demo;
    doc.gpus[2] = demo->gpus[1];
    snprintf(doc.gpus[2].host, sizeof doc.gpus[2].host, "nas");
    snprintf(doc.gpus[2].name, sizeof doc.gpus[2].name, "Tesla P4");
    doc.ngpus = 3;
    doc_seq++;
    load_cards("cards.json");
    load_layout("gpu-fan.json");
    run(600);
    ui_show_tab("GPU fan");
    run(600);
    shot("gpu-fan");
    const card_val_t *ws = cards_find(&cards_doc, "gf-ws-graph");
    if (!ws || ws->f < 0 || !cards_find(&cards_doc, "gf-srv-bar") || cards_find(&cards_doc, "gf-srv-bar")->f != 0) {
        return fail("cards.json lacks the workstation's or the server's fan");
    }
    char wl1[24], sl2[48], nl2[48];
    snprintf(wl1, sizeof wl1, "Fan %s", ws->v);
    snprintf(sl2, sizeof sl2, "%d°  " LV_SYMBOL_BULLET "  %d W", doc.gpus[1].temp, doc.gpus[1].power);
    snprintf(nl2, sizeof nl2, "-- fan  " LV_SYMBOL_BULLET "  %d proc", doc.gpus[2].procs); // 1 proc
    lv_obj_t *tab = shown_tab(tv), *c[6];
    for (int i = 0; i < 6; i++) {
        c[i] = lv_obj_get_child(tab, i);
    }
    if (!find_class(c[0], &lv_chart_class) || !find_class(c[0], &lv_arc_class) || !find(c[0], wl1, false) ||
        !find_class(c[1], &lv_bar_class) || !find(c[1], "Fan 0%", false) || !find(c[1], sl2, false) ||
        !find(c[2], "Fan --", false) || !find(c[2], nl2, false) || !find(c[4], "0%", false) || !find(c[5], "--", false)) {
        return fail("a GPU card doesn't show its fan (spinning, at 0 %, or with no reading)");
    }
    if (!cards_fit(tv) || !gauges_clear(tv) || !gauges_round(tv)) {
        return fail("a GPU card with a fan slot doesn't fit its contents");
    }
    // update() and values() each redraw the fan from what they keep: a new panel document leaves it; the agent
    // offline reads "--", and back online the fan returns without a new cards document.
    doc.gpus[0].up = false;
    doc_seq++;
    run(600);
    if (!find(c[0], "Fan --", false) || !find(c[1], "Fan 0%", false)) {
        return fail("an offline GPU's fan shows a reading, or a panel document lost another's fan");
    }
    doc.gpus[0].up = true;
    doc_seq++;
    run(600);
    if (!find(c[0], wl1, false)) {
        return fail("a GPU's fan didn't come back with its agent");
    }
    // C2a: stale cards data greys the fan's own objects (the ring; line 1's text), and the next good poll restores them.
    card_val_t *fe = (card_val_t *)cards_find(&cards_doc, "gf-ws-graph");
    lv_obj_t *fa = find_class(c[0], &lv_arc_class), *fl = find(c[0], wl1, false);
    if (!fe || !fa || !fl) {
        return fail("the fan-gauge card lacks its ring or line 1");
    }
    fe->st = CV_STALE;
    cards_seq++;
    run(600);
    if (lv_obj_get_style_opa(fa, 0) != LV_OPA_60 || lv_obj_get_style_opa(fl, 0) != LV_OPA_60) {
        return fail("stale cards data doesn't grey the fan's ring and line 1");
    }
    load_cards("cards.json");
    run(600);
    if (lv_obj_get_style_opa(fa, 0) != LV_OPA_COVER || lv_obj_get_style_opa(fl, 0) != LV_OPA_COVER) {
        return fail("the fan's ring and line 1 stay grey after a good poll");
    }

    // Every other size: 1x3 gauges (one small, over the fan graph), a 1x2 fan bar (from f) under the temperature, a
    // 1x2 fan graph drawn as a bar, 1x1 and 2x2 numbers, a 2x2 fan line 2.
    ui_show_tab("GPU fan 2");
    run(600);
    shot("gpu-fan-2");
    tab = shown_tab(tv);
    lv_obj_t *bar12 = find_class(lv_obj_get_child(tab, 2), &lv_bar_class), *srv12 = lv_obj_get_child(tab, 4);
    const card_val_t *w12 = cards_find(&cards_doc, "gf-ws-1x2");
    if (!bar12 || !w12 || lv_bar_get_value(bar12) != (int)(w12->f * 100 + 0.5f) ||
        !find_class(srv12, &lv_bar_class) || find_class(srv12, &lv_chart_class) ||
        !find(lv_obj_get_child(tab, 3), "Fan --", false)) {
        return fail("a 1x2 GPU card's fan bar isn't its fraction, or its fan graph isn't a bar");
    }
    if (!cards_fit(tv) || !gauges_clear(tv) || !gauges_round(tv)) {
        return fail("a smaller GPU card with a fan slot doesn't fit its contents");
    }
    // A small value change across 3 o'clock, where the ellipse lies inside the patch LVGL redraws for a round arc:
    // only a whole redraw of the gauge leaves no marks.
    fan_entries("80%", 0.80f, false);
    run(600);
    fan_entries("86%", 0.86f, false);
    run(600);
    if (!redraw_clean()) {
        return fail("a gauge's small value change left pixels a full redraw doesn't draw");
    }
    // The widest gauge number: every fan at 100 %.
    fan_entries("100%", 1.0f, false);
    run(600);
    shot("gpu-fan-full");
    if (!redraw_clean()) { // the fans' gauges just changed value
        return fail("a gauge's value change left pixels a full redraw doesn't draw");
    }
    if (!find(lv_obj_get_child(tab, 1), "100%", false) || !cards_fit(tv) || !gauges_clear(tv) || !gauges_round(tv)) {
        return fail("a fan gauge at 100 % doesn't fit");
    }
    ui_show_tab("GPU fan");
    run(600);
    if (!cards_fit(tv) || !gauges_clear(tv) || !gauges_round(tv)) {
        return fail("a 2x3 fan gauge at 100 % doesn't fit");
    }
    // An old agent: no fan anywhere ("--"), no fan history.
    load_cards("cards.json");
    fan_entries("--", -1, true);
    run(600);
    shot("gpu-fan-old-agent");
    if (!find(c[0], "Fan --", false) || !find(c[0], "--", false) || !find(c[1], "Fan --", false) ||
        !find(c[4], "--", false) || !cards_fit(tv) || !gauges_clear(tv) || !gauges_round(tv)) {
        return fail("a GPU card from an old agent doesn't read \"Fan --\"");
    }
    return 0;
}

// The radar card's red takeover frame (a 4 px WX_RED border), hidden or not, or NULL.
static lv_obj_t *find_frame(lv_obj_t *o)
{
    if (lv_obj_get_style_border_width(o, 0) == 4 && lv_color_eq(lv_obj_get_style_border_color(o, 0), lv_color_hex(WX_RED))) {
        return o;
    }
    for (uint32_t i = 0; i < lv_obj_get_child_count(o); i++) {
        lv_obj_t *f = find_frame(lv_obj_get_child(o, (int32_t)i));
        if (f) {
            return f;
        }
    }
    return NULL;
}

static uint32_t tab_index(const char *name)
{
    int i = 0;
    while (i < lay_doc.ntabs && strcmp(lay_doc.tabs[i].name, name)) {
        i++;
    }
    return (uint32_t)i;
}

// takeover-no-radar: a Tornado Warning with no radar card in the layout. The notice still shows over the
// tab that's up, its words open the alert sheet, and its OK acknowledges it; there's no radar tab to jump
// to. Then an escalation, and a layout with a radar card while that notice is up: the notice survives the
// rebuild, the panel jumps to the radar's tab without waiting for a new document, the new card has its red
// frame, and a rebuild that moves the radar card to a tab of another name jumps again. After OK neither the
// radar card moving back, nor it going and coming back, nor a document moves the tab (R6).
static int no_radar(void)
{
    lv_obj_t *tv = find_class(lv_screen_active(), &lv_tabview_class);
    tap("OK"); // the newest note's pop-up
    ui_show_tab("Planka");
    run(600);
    uint32_t tab = lv_tabview_get_tab_active(tv);
    load_weather("warning.json");
    run(600);
    if (!find(lv_layer_top(), "National Weather Service", true)) {
        return fail("no takeover notice without a radar card");
    }
    if (lv_tabview_get_tab_active(tv) != tab) {
        return fail("the takeover changed the tab with no radar card");
    }
    shot("takeover-no-radar");
    tap_at("Tornado Warning for your area", true);
    if (!find(lv_layer_top(), "Weather alerts", true)) {
        return fail("the notice's words didn't open the alert sheet without a radar card");
    }
    tap("Close");
    tap("OK");
    load_weather("warning.json"); // a later document doesn't bring it back
    run(500);
    if (find(lv_layer_top(), "National Weather Service", true) || lv_tabview_get_tab_active(tv) != tab) {
        return fail("OK didn't clear the notice without a radar card, or the tab changed");
    }

    wx.alerts[0].lvl = 1; // tornado observed
    wx_seq++;
    run(500);
    if (!find(lv_layer_top(), "National Weather Service", true) || lv_tabview_get_tab_active(tv) != tab) {
        return fail("the escalation didn't show the notice without a radar card, or the tab changed");
    }
    load_layout("default.json"); // a radar card arrives, and no new weather document: the jump comes anyway
    run(1000);
    if (!find(lv_layer_top(), "National Weather Service", true)) {
        return fail("a layout change took the takeover notice down");
    }
    if (lv_tabview_get_tab_active(tv) != tab_index("Weather")) {
        return fail("the takeover didn't jump to the new radar card's tab");
    }
    lv_obj_t *red = find_frame(lv_screen_active());
    if (!red || lv_obj_is_hidden(red)) {
        return fail("the new radar card has no red frame during the takeover");
    }
    shot("takeover-jump");
    load_layout("radar-moved.json"); // the radar card moves to a "Radar" tab: the rebuild falls back to tab 0,
    run(1000);                       // and the unacknowledged notice follows the card there
    if (lv_tabview_get_tab_active(tv) != tab_index("Radar")) {
        return fail("the takeover didn't follow the radar card to its renamed tab");
    }
    tap("OK");
    ui_show_tab("Planka");
    run(600);
    load_layout("default.json"); // after OK the radar card moves back to "Weather": the tab stays
    run(600);
    if (lv_tabview_get_tab_active(tv) != tab_index("Planka")) {
        return fail("after OK, a moved radar card pulled the tab to it");
    }
    load_layout("noradar.json"); // ...and it goes and comes back, and a document arrives: the tab stays (R6)
    run(600);
    load_layout("default.json");
    run(600);
    wx_seq++;
    run(500);
    if (lv_tabview_get_tab_active(tv) != tab_index("Planka")) {
        return fail("after OK, the takeover pulled the tab back to the radar");
    }
    return 0;
}

// Every object of class cls under o (depth first, hidden ones too) into out, at most n; returns how many.
static int all_of(lv_obj_t *o, const lv_obj_class_t *cls, lv_obj_t **out, int n)
{
    int k = 0;
    for (uint32_t i = 0; i < lv_obj_get_child_count(o) && k < n; i++) {
        lv_obj_t *c = lv_obj_get_child(o, (int32_t)i);
        if (lv_obj_check_type(c, cls)) {
            out[k++] = c;
        }
        k += all_of(c, cls, out + k, n - k);
    }
    return k;
}

// In physical pixels: the panel's are PANEL_ASPECT wider than tall (the sim's are square, so the clock looks tall).
static double from_dial_centre(double x, double y) { return hypot((x - 240) * PANEL_ASPECT, y - 240); }

// The physical radius on which the pixels along the ray at deg (clockwise from up), r0..r1 from the dial's centre,
// centre: each weighted by its gold (R - B) or by how much brighter it is than the dial's #040814.
static double ray_centre(double deg, double r0, double r1, bool gold)
{
    double sum = 0, wsum = 0, a = deg * M_PI / 180;
    for (double r = r0; r <= r1; r += 0.25) {
        int x = 240 + (int)lround(r * sin(a) / PANEL_ASPECT), y = 240 - (int)lround(r * cos(a));
        uint32_t p = fb[y * W + x];
        int R = p >> 16 & 0xff, G = p >> 8 & 0xff, B = p & 0xff, w = gold ? R - B : R + G + B - (4 + 8 + 20);
        if (w > 0) {
            sum += w * from_dial_centre(x, y);
            wsum += w;
        }
    }
    return wsum ? sum / wsum : 0;
}

// Every shown label of the clock's column (from x 480) is one line inside x 500..770; uncut: and not cut with "...".
static bool column_fits(lv_obj_t *view, bool uncut)
{
    lv_obj_t *o[96];
    int n = all_of(view, &lv_label_class, o, 96);
    for (int i = 0; i < n; i++) {
        lv_area_t a;
        lv_obj_get_coords(o[i], &a);
        if (!lv_obj_is_visible(o[i]) || a.x1 < 480) {
            continue;
        }
        const char *t = lv_label_get_text(o[i]);
        const lv_font_t *f = lv_obj_get_style_text_font(o[i], 0);
        lv_point_t sz;
        lv_text_get_size(&sz, t, f, lv_obj_get_style_text_letter_space(o[i], 0), 0, LV_COORD_MAX, LV_TEXT_FLAG_NONE);
        if (a.x1 < 500 || a.x2 > 770 || lv_obj_get_height(o[i]) > lv_font_get_line_height(f) ||
            (uncut && (strstr(t, "...") || sz.x > lv_obj_get_content_width(o[i])))) {
            fprintf(stderr, "sim: the clock's \"%s\" is at %d,%d-%d,%d, %d px of text\n", t, (int)a.x1, (int)a.y1,
                    (int)a.x2, (int)a.y2, (int)sz.x);
            return false;
        }
    }
    return true;
}

// The clock's shown overhead lines: how many of astro_doc's names they hold in all (a name twice counts twice), on
// how many lines, and the "+N" after them (0: none).
static int overhead_shown(lv_obj_t *view, int *lines, int *more)
{
    lv_obj_t *o[96];
    int n = all_of(view, &lv_label_class, o, 96), names = 0;
    *lines = *more = 0;
    for (int i = 0; i < n; i++) {
        const char *t = lv_label_get_text(o[i]), *plus = strstr(t, "  +");
        int k = 0;
        for (int j = 0; lv_obj_is_visible(o[i]) && j < astro_doc.nover; j++) {
            k += strstr(t, astro_doc.over[j]) != NULL;
        }
        *lines += k > 0;
        names += k;
        *more = plus && k ? atoi(plus + 3) : *more;
    }
    return names;
}

static bool covered(bool hidden);

// The clock shows astro_doc: its phase, "% lit", sunrise and heading, every overhead name on at most 4 lines or in
// the "+N", and its column fits, uncut.
static bool shows_doc(lv_obj_t *view)
{
    char lit[16];
    int lines, more;
    snprintf(lit, sizeof lit, "%d %% lit", (int)lroundf(astro_doc.illum * 100));
    return column_fits(view, true) && find(view, astro_doc.phase, false) && find(view, lit, false) &&
           find(view, astro_doc.rise, false) && find(view, astro_doc.label, false) &&
           overhead_shown(view, &lines, &more) + more == astro_doc.nover && lines <= 4;
}

// The astronomical clock, opened directly (the tour and the status-bar tap come later) over the demo's Greenwich sky
// at 8:45 PM, the mockup's time, then with no location, then at 12:45 PM the next day (the daytime sky), then with
// the longest text the contract allows, and stale. The sim's zone is CST: its clock is set to read Greenwich's time.
static int astro(void)
{
    lv_obj_t *view = lv_obj_get_child(lv_screen_active(), -1), *o[96];
    if (!lv_obj_is_hidden(view) || astro_view_is_open()) {
        return fail("the clock isn't the screen's last child, hidden until opened");
    }
    clock_back = -(6 * 3600 + 45 * 60); // DEMO_NOW is 2:00 PM; the night sky is drawn at DEMO_NOW + 45 min (run.sh)
    run(600);
    astro_view_open(false);
    run(600);
    shot("astro-night");
    if (lv_obj_is_hidden(view) || !astro_view_is_open() || !covered(true)) {
        return fail("the clock didn't open, or left what it covers shown");
    }
    // Round on the glass (Review Focus 1): across 12 diameters between the ticks, in physical pixels, the gold rim is
    // 379 px (r 189.5) and the day/night ring 445 (r 222.5), each to within 1 px. (Diameters, so it doesn't matter
    // which half pixel LVGL centres them on.)
    for (int k = 0; k < 12; k++) {
        double deg = 7.5 + 15 * k, rim = ray_centre(deg, 184, 195, true) + ray_centre(deg + 180, 184, 195, true),
               ring = ray_centre(deg, 219, 228, false) + ray_centre(deg + 180, 219, 228, false);
        if (fabs(rim - 379) > 1 || fabs(ring - 445) > 1) {
            fprintf(stderr, "sim: at %.1f deg the rim is %.2f across, the day/night ring %.2f\n", deg, rim, ring);
            return fail("the rim or the day/night ring isn't round on the glass");
        }
    }
    // The hands' lines (gold #e2c27a): the farthest point is the minute hand's tip, at r 176 and, at 8:45, on the left.
    double tip = 0, tx = 0, ty = 0;
    int n = all_of(view, &lv_line_class, o, 96);
    for (int i = 0; i < n; i++) {
        lv_area_t a;
        lv_obj_get_coords(o[i], &a);
        const lv_point_precise_t *p = lv_line_get_points(o[i]);
        for (uint32_t k = 0; lv_color_eq(lv_obj_get_style_line_color(o[i], 0), lv_color_hex(0xe2c27a)) &&
                             k < lv_line_get_point_count(o[i]); k++) {
            double r = from_dial_centre(a.x1 + p[k].x, a.y1 + p[k].y);
            if (r > tip) {
                tip = r, tx = a.x1 + p[k].x, ty = a.y1 + p[k].y;
            }
        }
    }
    if (tip > 176 || tip < 175 || tx > 80 || fabs(ty - 240) > 1) {
        fprintf(stderr, "sim: the minute hand's tip is at %.0f,%.0f (r %.1f)\n", tx, ty, tip);
        return fail("the minute hand's tip isn't at r 176 pointing at 45 minutes");
    }
    // The hour hand's ring (border #e2c27a) is centred at r 118.
    int rings = 0;
    n = all_of(view, &lv_obj_class, o, 96);
    for (int i = 0; i < n; i++) {
        lv_area_t a;
        lv_obj_get_coords(o[i], &a);
        double r = from_dial_centre((a.x1 + a.x2 + 1) / 2.0, (a.y1 + a.y2 + 1) / 2.0);
        if (lv_obj_get_style_border_width(o[i], 0) &&
            lv_color_eq(lv_obj_get_style_border_color(o[i], 0), lv_color_hex(0xe2c27a))) {
            rings++;
            if (fabs(r - 118) > 1) {
                fprintf(stderr, "sim: the hour hand's ring is centred at r %.1f\n", r);
                return fail("the hour hand's ring isn't centred at r 118");
            }
        }
    }
    if (!rings) {
        return fail("the clock has no hour ring");
    }
    lv_obj_t *sky = NULL;
    n = all_of(view, &lv_image_class, o, 96);
    for (int i = 0; i < n; i++) {
        sky = lv_obj_get_width(o[i]) == SKY_FIT_W ? o[i] : sky;
    }
    int lines, more;
    if (!sky || lv_obj_is_hidden(sky) || !find(view, "8:45", false) || !find(view, "OVERHEAD NOW", false) ||
        !shows_doc(view)) {
        return fail("the clock doesn't show the sky, or its column doesn't fit or show the night document");
    }

    astro_st = 404; // A3: no location, no sky; the time and date stay
    run(600);
    shot("astro-nolocation");
    if (!find(view, "Set WEATHER_LAT/LON for the sky", false) || find(view, "Sunrise", false) ||
        !find(view, "8:45", false) || !lv_obj_is_hidden(sky) || !column_fits(view, true)) {
        return fail("with no location the clock doesn't say so in place of its rows, or still shows a sky");
    }

    astro_st = 200; // the day: the sky tinted by daylight, the sun drawn, the list for tonight at 9 PM
    clock_back = -(22 * 3600 + 45 * 60);
    load_astro("day");
    run(600);
    shot("astro-day");
    if (lv_obj_is_hidden(sky) || !find(view, "12:45", false) || !find(view, "TONIGHT AT 9 PM", false) ||
        !shows_doc(view)) {
        return fail("the clock doesn't show the day sky, or its column doesn't fit or show the day document");
    }

    // Review Focus 5: a 23-byte phase, "—" times and 12 overhead names of 23 bytes: all on their lines, "+N" last.
    snprintf(astro_doc.phase, sizeof astro_doc.phase, "Waning crescent, faint.");
    snprintf(astro_doc.moonrise, sizeof astro_doc.moonrise, "\xe2\x80\x94");
    astro_doc.rise[0] = '\0';
    astro_doc.nover = ASTRO_OVER_MAX;
    for (int i = 0; i < ASTRO_OVER_MAX; i++) {
        snprintf(astro_doc.over[i], sizeof astro_doc.over[i], "Constellation name no%02d", i + 1);
    }
    astro_seq++;
    run(600);
    int names = overhead_shown(view, &lines, &more);
    if (lines > 4 || !more || names + more != ASTRO_OVER_MAX || !column_fits(view, false)) {
        fprintf(stderr, "sim: %d overhead names on %d lines, then \"+%d\"\n", names, lines, more);
        return fail("12 long overhead names don't wrap to 4 lines and \"+N\", or the column doesn't fit");
    }

    astro_age = 11 * 60 * 1000; // 10 min without a good poll: the sky greys
    run(600);
    if (lv_obj_get_style_image_opa(sky, 0) != LV_OPA_60) {
        return fail("the sky doesn't grey after 10 minutes without a good poll");
    }
    astro_age = 2000;
    run(600);
    if (lv_obj_get_style_image_opa(sky, 0) != LV_OPA_COVER) {
        return fail("the sky stays grey after a good poll");
    }

    // Ten minutes on, the hands have moved: what the partial redraws left must equal a full repaint (a shadow clipped
    // at its redraw margin leaves trails).
    static uint32_t moved[W * H];
    clock_back -= 10 * 60;
    run(600);
    lv_refr_now(NULL);
    memcpy(moved, fb, sizeof fb);
    lv_obj_invalidate(lv_screen_active());
    lv_refr_now(NULL);
    int trail = 0;
    for (int i = 0; i < W * H; i++) {
        trail += moved[i] != fb[i];
    }
    if (trail) {
        fprintf(stderr, "sim: %d pixels differ from a full repaint after the hands moved\n", trail);
        return fail("moving the hands leaves trails");
    }

    time_ok = false; // the clock isn't set: "--:--" and no hands
    run(600);
    bool hands = false;
    n = all_of(view, &lv_line_class, o, 96);
    for (int i = 0; i < n; i++) {
        hands |= lv_obj_is_visible(o[i]);
    }
    time_ok = true;
    if (hands || !find(view, "--:--", false)) {
        return fail("the clock shows hands, or no \"--:--\", before the time is set");
    }
    run(600);

    astro_view_close(true); // shows what it covered at once, fades out, then hides
    if (astro_view_is_open() || !covered(false)) {
        return fail("the clock still says it's open while it fades out, or keeps what it covered hidden");
    }
    run(500);
    if (!lv_obj_is_hidden(view) || lv_obj_get_style_opa(view, 0) != LV_OPA_COVER) {
        return fail("the clock didn't hide after fading out");
    }
    return 0;
}

// The open clock hides the screen's other children (the status bar, the tabs): LVGL would keep redrawing them under it.
static bool covered(bool hidden)
{
    lv_obj_t *scr = lv_screen_active();
    for (uint32_t i = 0; i + 1 < lv_obj_get_child_count(scr); i++) { // the clock is the last child
        if (lv_obj_is_hidden(lv_obj_get_child(scr, (int32_t)i)) != hidden) {
            return false;
        }
    }
    return true;
}

// At the end of the tour, on the Weather tab: the clock opened over the radar card stops its loop (nothing redraws
// under the clock), and after it closes the loop runs again.
static int clock_over_radar(void)
{
    lv_obj_t *tv = find_class(lv_screen_active(), &lv_tabview_class), *img = find_class(shown_tab(tv), &lv_image_class);
    if (!img) {
        return fail("no radar image on the tab");
    }
    astro_view_open(true);
    run(600); // past the fade-in, and the radar's next tick (it rewinds once when it stops being visible)
    if (!covered(true)) {
        return fail("the open clock left the status bar or the tabs shown");
    }
    const void *src = lv_image_get_src(img);
    run(3000);
    if (lv_image_get_src(img) != src) {
        return fail("the radar loop ran under the open clock");
    }
    astro_view_close(false);
    run(600);
    src = lv_image_get_src(img);
    for (int t = 0; t < 30 && lv_image_get_src(img) == src; t++) {
        run(100);
    }
    if (!covered(false) || lv_image_get_src(img) == src) {
        return fail("after the clock closed, the tabs aren't back or the radar loop didn't run again");
    }
    return 0;
}

// --- The idle tour, at the very end of the walk-through (its shots come last, so every frame before stays as it is) ---
#define IDLE_MS (5 * 60 * 1000) // the tour starts after this long without a touch

static uint32_t step_at; // when the tour's step on screen began (to 5 ms)

// The tour's catcher is the top layer's first child (under the notices, toasts and sheets), shown only while it runs.
static bool touring(void) { return !lv_obj_is_hidden(lv_obj_get_child(lv_layer_top(), 0)); }

static int step(lv_obj_t *tv) { return astro_view_is_open() ? -1 : (int)lv_tabview_get_tab_active(tv); } // -1: the clock

static void jump(uint32_t ms) // the clock jumps ahead: whatever falls due runs once
{
    now_ms += ms;
    run(5);
}

// 1 s short of `idle` ms after t0 (the sim's clock) there's no tour; within 1.5 s more it starts, on step `first`
// (see step()).
static bool tour_starts_idle(uint32_t t0, uint32_t idle, int first)
{
    jump(t0 + idle - 1000 - now_ms);
    if (touring() || astro_view_is_open()) {
        return false;
    }
    for (int t = 0; t < 1500 && !touring(); t += 5) {
        run(5);
    }
    step_at = now_ms;
    return touring() && step(find_class(lv_screen_active(), &lv_tabview_class)) == first;
}

static bool tour_starts_at(uint32_t t0) { return tour_starts_idle(t0, IDLE_MS, -1); }

static bool tour_starts(void) { return tour_starts_at(now_ms - lv_display_get_inactive_time(NULL)); } // the last touch

static bool popup_gone(void) // run until the pop-up goes (30 s after it came, unanswered)
{
    for (int t = 0; t < 31000 && find(lv_layer_top(), "A new note", false); t += 5) {
        run(5);
    }
    return !find(lv_layer_top(), "A new note", false);
}

// The tour moves from step `from` to `to` (see step()) ms after its last move: not 1 s before, and by 1 s after.
static bool tour_moves(lv_obj_t *tv, int from, int to, uint32_t ms)
{
    jump(step_at + ms - 1000 - now_ms);
    if (!touring() || step(tv) != from) {
        return false;
    }
    while (step(tv) == from && now_ms < step_at + ms + 1000) {
        run(5);
    }
    step_at = now_ms;
    return touring() && step(tv) == to;
}

static void tap_time(void) // the status bar's time, 8 px right of its text (its tap area is wider)
{
    lv_area_t a;
    lv_obj_get_coords(lv_obj_get_child(lv_obj_get_child(lv_screen_active(), 0), 0), &a);
    press(a.x2 + 8, (a.y1 + a.y2) / 2);
}

static void new_note(void) // a new unread note: it pops up
{
    memmove(&doc.notes[1], &doc.notes[0], sizeof doc.notes[0] * (PANEL_MAX_NOTES - 1));
    doc.notes[0] = (panel_note_t){.id = doc.notes[1].id + 1, .src = "Sim", .msg = "A new note", .lvl = "info"};
    doc.nnotes += doc.nnotes < PANEL_MAX_NOTES;
    doc_seq++;
}

static void new_warning(const char *key) // a tornado warning not seen before: the takeover
{
    snprintf(wx.alerts[0].key, sizeof wx.alerts[0].key, "%s", key);
    wx_seq++;
    run(300);
}

// The layout is Homelab, Planka, Weather; the escalated warning's notice is up.
static int tour(void)
{
    lv_obj_t *tv = find_class(lv_screen_active(), &lv_tabview_class), *view = lv_obj_get_child(lv_screen_active(), -1);
    lv_obj_t *content = lv_tabview_get_content(tv), *nas;
    lv_area_t a;
    tap("OK");
    tap("Planka");     // the tab before the tour
    load_astro("day"); // the panel reads 2 PM: the day sky
    if (!tour_starts()) {
        return fail("5 min without a touch didn't start the tour on the clock, or it started sooner");
    }
    run(400); // a lap, shot once each step has faded or slid in (and once mid-slide)
    shot("tour-clock");
    bool lap = tour_moves(tv, -1, 0, 60000);
    run(400);
    shot("tour-homelab");
    lap = lap && tour_moves(tv, 0, 1, 20000);
    run(100);
    shot("tour-slide");
    run(500);
    shot("tour-planka");
    lap = lap && tour_moves(tv, 1, 2, 20000);
    run(600);
    shot("tour-weather");
    if (!lap || !tour_moves(tv, 2, -1, 20000)) {
        return fail("the tour didn't show the clock 60 s, then each tab 20 s in order, then the clock again");
    }
    run(600); // the clock, over Weather
    press(400, 240);
    if (touring() || astro_view_is_open() || !lv_obj_is_hidden(view) || !covered(false) || ui_sheet_is_open() ||
        lv_tabview_get_tab_active(tv) != 1) {
        return fail("a tap on the tour's clock didn't only stop the tour and go back to the tab from before it");
    }

    // A tap on a card mid-tour only stops the tour, on that tab; the next tap works as always. (From Planka the clock
    // gives way to Homelab at once, under its fade: no slide across the tabs.)
    if (!tour_starts() || !tour_moves(tv, -1, 0, 60000)) {
        return fail("the tour didn't start again 5 min after the last touch");
    }
    if (lv_obj_get_scroll_x(content) != 0) {
        return fail("the clock's step to the first tab slid across the tabs");
    }
    run(1000);
    tap("NAS ALERTS");
    if (touring() || ui_sheet_is_open() || lv_tabview_get_tab_active(tv) != 0) {
        return fail("a tap on a card during the tour pressed it, or didn't stop the tour on its tab");
    }
    tap("NAS ALERTS");
    if (!ui_sheet_is_open()) {
        return fail("after the tour, a tap on a card doesn't open its sheet");
    }
    tap("Close");

    // Review Focus 4: a tap mid-slide (on the NAS card sliding away) and mid-fade are swallowed too.
    if (!tour_starts() || !tour_moves(tv, -1, 0, 60000) || !tour_moves(tv, 0, 1, 20000)) {
        return fail("the tour didn't start again, or didn't slide from Homelab to Planka");
    }
    run(50);
    nas = find(lv_screen_active(), "NAS ALERTS", false);
    int32_t x = lv_obj_get_scroll_x(content);
    if (!nas || x <= 0 || x >= W) {
        fprintf(stderr, "sim: the tabs are at x %d\n", (int)x);
        return fail("the slide from Homelab to Planka isn't under way");
    }
    lv_obj_get_coords(nas, &a);
    press((a.x1 + a.x2) / 2, (a.y1 + a.y2) / 2);
    if (touring() || ui_sheet_is_open() || lv_tabview_get_tab_active(tv) != 1 || lv_obj_get_scroll_x(content) != W) {
        return fail("a tap during a tab slide pressed a card, or didn't end the tour on the tab it slid to");
    }
    if (!tour_starts()) {
        return fail("the tour didn't start again");
    }
    run(100);
    lv_opa_t opa = lv_obj_get_style_opa(view, 0);
    if (opa == LV_OPA_TRANSP || opa == LV_OPA_COVER) {
        return fail("the tour's clock isn't fading in");
    }
    press(400, 240);
    if (touring() || astro_view_is_open() || !lv_obj_is_hidden(view) || ui_sheet_is_open() ||
        lv_tabview_get_tab_active(tv) != 1) {
        return fail("a tap during the clock's fade didn't only stop the tour and go back to the tab");
    }

    // The clock button: the status bar's time opens the clock, no tour takes it over, a tap closes it.
    tap_time();
    if (!astro_view_is_open() || touring()) {
        return fail("a tap on the status bar's time didn't open the clock");
    }
    jump(6 * 60 * 1000);
    if (!astro_view_is_open() || touring()) {
        return fail("the tour took over the clock opened by hand");
    }
    press(400, 240);
    if (astro_view_is_open() || !lv_obj_is_hidden(view) || ui_sheet_is_open()) {
        return fail("a tap on the clock opened by hand didn't close it, or pressed what's under it");
    }

    // No tour under a sheet or a pop-up, and the 5 min count only from when it goes (answered or not); a pop-up mid-tour
    // stops it.
    tap_at(LV_SYMBOL_BELL, true);
    jump(6 * 60 * 1000);
    if (touring() || astro_view_is_open()) {
        return fail("the tour started under an open sheet");
    }
    tap("Close");
    jump(IDLE_MS - 10000 - lv_display_get_inactive_time(NULL));
    new_note();
    run(600);
    if (!find(lv_layer_top(), "A new note", false)) {
        return fail("the new note didn't pop up");
    }
    jump(20000);
    if (touring() || astro_view_is_open()) {
        return fail("the tour started under a notification pop-up");
    }
    if (!popup_gone() || !tour_starts_at(now_ms)) {
        return fail("the tour didn't wait 5 min after an unanswered pop-up went");
    }
    new_note();
    run(600);
    if (touring() || astro_view_is_open() || !find(lv_layer_top(), "A new note", false)) {
        return fail("a notification pop-up didn't pause the tour");
    }
    if (!popup_gone() || !tour_starts_at(now_ms)) {
        return fail("the tour didn't wait 5 min after a pop-up that paused it went unanswered");
    }
    new_note();
    run(600);
    tap("OK");
    if (!tour_starts()) {
        return fail("the tour didn't wait 5 min after the pop-up was answered");
    }

    // Review Focus 1: a takeover mid-tour shows its notice, closes the clock, stops the tour and jumps to the radar;
    // no tour while the notice is up, and none until 5 min after its OK. Then the same over the clock opened by hand,
    // and a warning that ends without an OK. The bar and the tabs are back after each.
    run(600); // the clock fully open: the bar and the tabs hidden under it
    new_warning("tour-1");
    if (!find(lv_layer_top(), "National Weather Service", true) || touring() || astro_view_is_open() ||
        !lv_obj_is_hidden(view) || !covered(false) || lv_tabview_get_tab_active(tv) != tab_index("Weather")) {
        return fail("a takeover during the tour didn't show its notice, close the clock, stop the tour and jump");
    }
    clock_back += 3600; // for the shot only: the tour's waits have run the clock an hour, past the demo warning's end
    run(1200);
    shot("tour-takeover");
    clock_back -= 3600;
    jump(6 * 60 * 1000);
    if (touring() || astro_view_is_open()) {
        return fail("the tour started under the takeover notice");
    }
    tap("OK");
    if (!tour_starts()) {
        return fail("the tour didn't wait 5 min after the takeover notice's OK");
    }
    press(400, 240);
    tap_time();
    new_warning("tour-2");
    if (!find(lv_layer_top(), "National Weather Service", true) || astro_view_is_open() || !lv_obj_is_hidden(view) ||
        !covered(false)) {
        return fail("a takeover didn't close the clock opened by hand");
    }
    jump(6 * 60 * 1000);
    wx.nalerts = 0; // the warning ends without an OK: the notice goes, and only then do the 5 min start
    wx_seq++;
    for (int t = 0; t < 1000 && find(lv_layer_top(), "National Weather Service", true); t += 5) {
        run(5);
    }
    if (find(lv_layer_top(), "National Weather Service", true) || !tour_starts_at(now_ms)) {
        return fail("the tour didn't wait 5 min after the takeover notice went unanswered");
    }

    // A layout change mid-tour stops it, and the 5 min start again.
    load_layout("noradar.json");
    run(1000);
    if (touring() || astro_view_is_open()) {
        return fail("a layout change didn't stop the tour, or it started again at once");
    }
    load_layout("default.json");
    run(1000);

    // The layout's tour block (poked into the served document: Homelab, Planka, Weather).
    typedef struct { uint32_t idle, clock, tab; bool on, clk; uint8_t skip; } tour_cfg_t;
#define POKE(...) do { tour_cfg_t c_ = __VA_ARGS__;         lay_doc.tour_idle_ms = c_.idle; lay_doc.tour_clock_ms = c_.clock; lay_doc.tour_tab_ms = c_.tab;         lay_doc.tour_on = c_.on; lay_doc.tour_clock = c_.clk; lay_doc.tour_skip = c_.skip;         lay_seq++; run(1000); tv = find_class(lv_screen_active(), &lv_tabview_class); } while (0)
#define SHOW(i) do { lv_tabview_set_active(tv, (i), LV_ANIM_OFF); run(600); } while (0)
    // Clock off, Planka skipped, idle 60 s, 5 s a tab: from Weather it starts on Homelab, then Weather, then wraps
    // to Homelab over the switched-off clock; a tap on a card only stops it, on the tab it shows.
    POKE((tour_cfg_t){60000, 60000, 5000, true, false, 0x02});
    SHOW(2);
    if (!tour_starts_idle(now_ms - lv_display_get_inactive_time(NULL), 60000, 0) || astro_view_is_open()) {
        return fail("the layout's idle time didn't start the tour on its first wanted tab, or the clock was open");
    }
    if (!tour_moves(tv, 0, 2, 5000) || !tour_moves(tv, 2, 0, 5000)) {
        return fail("the layout's tab time, skipped tab or switched-off clock weren't honoured");
    }
    run(600);
    tap("NAS ALERTS");
    if (touring() || ui_sheet_is_open() || astro_view_is_open() || lv_tabview_get_tab_active(tv) != 0) {
        return fail("a tap during a clock-less tour pressed a card, or didn't stop it on the tab showing");
    }
    // Clock on, every tab skipped: only the clock, and it stays on past its time; a tap returns to the tab before.
    POKE((tour_cfg_t){60000, 10000, 5000, true, true, 0x07});
    SHOW(1);
    if (!tour_starts_idle(now_ms - lv_display_get_inactive_time(NULL), 60000, -1)) {
        return fail("a clock-only tour didn't start on the clock");
    }
    jump(20000);
    if (!touring() || !astro_view_is_open()) {
        return fail("a clock-only tour left the clock");
    }
    press(400, 240);
    if (touring() || astro_view_is_open() || lv_tabview_get_tab_active(tv) != 1) {
        return fail("a tap on a clock-only tour didn't go back to the tab from before it");
    }
    // Nothing to show, and the tour switched off: no tour well past the idle time.
    tour_cfg_t none[2] = {{60000, 10000, 5000, true, false, 0x07}, {60000, 10000, 5000, false, true, 0}};
    for (int i = 0; i < 2; i++) {
        POKE(none[i]);
        SHOW(1);
        jump(70000);
        run(1000);
        if (touring() || astro_view_is_open()) {
            return fail("a tour with no steps, or a switched-off one, started");
        }
    }
    // Clock off and the only step is the tab on screen: no tour, so no touch is swallowed.
    POKE((tour_cfg_t){60000, 10000, 5000, true, false, 0x06});
    SHOW(0);
    jump(70000);
    run(1000);
    if (touring()) {
        return fail("a tour whose only step is the tab already showing started");
    }
    tap("NAS ALERTS");
    if (!ui_sheet_is_open()) {
        return fail("a tap after a no-change tour was swallowed");
    }
    tap("Close");
    load_layout("default.json");
    run(1000);
    return 0;
}

// Loop states, at the very end (no shots). With a takeover active the radar loops past 3 loops and never dims; the clock
// is 15+ min past the newest frame by now, so the chip is amber; once the warning ends the conditions come back within a
// cycle and the red frame goes.
static int loop_states(void)
{
    lv_display_trigger_activity(NULL); // no idle tour during these ~52 s
    load_weather("warning.json");
    ui_show_tab("Weather");
    run(1500); // past any fade back to the radar
    if (time(NULL) - wx.frames[wx.nframes - 1] <= 900) {
        return fail("loop states: the walk should end 15+ min after the newest frame");
    }
    for (int t = 0; t < 30000; t += 100) {
        run(100);
        if (find(lv_screen_active(), "Humidity", true)) {
            return fail("the radar dimmed to the conditions during a takeover");
        }
    }
    lv_obj_t *chip = find(lv_screen_active(), "\xe2\x88\x92", true); // "−N min · 2:30 PM"
    if (!chip || !lv_color_eq(lv_obj_get_style_text_color(chip, 0), lv_color_hex(WX_AMBER))) {
        return fail("radar 15+ min old didn't turn the time chip amber");
    }
    load_weather("weather.json"); // the warning is over
    for (int t = 0; t < 20000 && !find(lv_screen_active(), "Humidity", true); t += 100) {
        run(100);
    }
    lv_obj_t *red = find_frame(lv_screen_active());
    if (!find(lv_screen_active(), "Humidity", true) || !red || !lv_obj_is_hidden(red)) {
        return fail("after the warning ended, the conditions didn't come back or the red frame stayed");
    }
    return 0;
}

// The takeover notice, the parent of the parent of its "National Weather Service" label, or NULL.
static lv_obj_t *takeover_notice(void)
{
    lv_obj_t *l = find(lv_layer_top(), "National Weather Service", true);
    return l ? lv_obj_get_parent(lv_obj_get_parent(l)) : NULL;
}

static bool overlap(const lv_area_t *a, const lv_area_t *b)
{
    return a->x1 <= b->x2 && b->x1 <= a->x2 && a->y1 <= b->y2 && b->y1 <= a->y2;
}

// Review Focus 4 (no shots): a toast and a notification pop-up that come up after the takeover notice stay behind it,
// and the toast stays readable, above it.
static int notice_on_top(void)
{
    lv_display_trigger_activity(lv_display_get_default()); // no idle tour
    load_weather("warning.json");
    new_warning("sim-top");
    ui_toast("Sim toast", COL_OK);
    // ui.c makes its toast once ("Marked done", long before): put it in front, as a toast made for the first time
    // now would be, then show one again.
    lv_obj_t *toast = find(lv_layer_top(), "Sim toast", false);
    if (toast) {
        lv_obj_move_foreground(toast);
    }
    ui_toast("Sim toast", COL_OK);
    lv_obj_t *n = takeover_notice();
    if (!n || !toast) {
        return fail("no takeover notice or toast");
    }
    if (lv_obj_get_index(n) < lv_obj_get_index(toast)) { // now, before the pop-up's own raise
        return fail("a toast came up over the takeover notice");
    }
    new_note();
    run(600);
    lv_obj_t *pop = find(lv_layer_top(), "A new note", false);
    if (!pop) {
        return fail("no pop-up over the takeover");
    }
    pop = lv_obj_get_parent(lv_obj_get_parent(pop));
    int32_t at = lv_obj_get_index(n);
    if (at < lv_obj_get_index(toast) || at < lv_obj_get_index(pop)) {
        return fail("a toast or pop-up came up over the takeover notice");
    }
    lv_area_t na, ta, pa;
    lv_obj_get_coords(n, &na);
    lv_obj_get_coords(toast, &ta);
    lv_obj_get_coords(pop, &pa);
    if (overlap(&ta, &na) || overlap(&ta, &pa) || ta.x1 < 0 || ta.y1 < 0 || ta.x2 >= W || ta.y2 >= H) {
        fprintf(stderr, "sim: the toast is at %d,%d-%d,%d, the notice %d,%d-%d,%d\n", (int)ta.x1, (int)ta.y1, (int)ta.x2,
                (int)ta.y2, (int)na.x1, (int)na.y1, (int)na.x2, (int)na.y2);
        return fail("a toast under the takeover notice can't be read");
    }
    return 0;
}

// Review Focus 5 (no shots): four alerts' chips share one row, the text box keeps its height, and the 4th chip, opened
// off the row's edge, scrolls into view.
static int sheet_four(void)
{
    lv_display_trigger_activity(lv_display_get_default()); // no idle tour
    wx.alerts[3] = wx.alerts[2];
    snprintf(wx.alerts[3].event, sizeof wx.alerts[3].event, "Flood Advisory");
    wx.nalerts = 4;
    wx_seq++;
    run(300);
    takeover_open_sheet(3);
    run(300); // its text arrives
    lv_obj_t *title = find(lv_layer_top(), "Weather alerts", true);
    lv_obj_t *box = title ? lv_obj_get_parent(title) : NULL;
    lv_obj_t *row = box ? lv_obj_get_child(box, 1) : NULL, *page = box ? lv_obj_get_child(box, 2) : NULL;
    if (!row || lv_obj_get_child_count(row) != 4) {
        return fail("the alert sheet with four alerts has no row of four chips");
    }
    lv_area_t b, a, first;
    lv_obj_get_coords(box, &b);
    lv_obj_get_coords(lv_obj_get_child(row, 0), &first);
    for (int i = 1; i < 4; i++) {
        lv_obj_get_coords(lv_obj_get_child(row, i), &a);
        if (a.y1 != first.y1) {
            return fail("the four alerts' chips aren't on one row");
        }
    }
    if (lv_obj_get_height(page) < 230) {
        fprintf(stderr, "sim: the alert sheet's text box is %d px tall\n", (int)lv_obj_get_height(page));
        return fail("four alerts' chips squeezed the alert sheet's text box");
    }
    lv_obj_t *chip = lv_obj_get_child(lv_obj_get_child(row, 3), 0);
    if (a.x1 < b.x1 || a.x2 > b.x2 || a.y1 < b.y1 || a.y2 > b.y2 || lv_obj_get_style_border_opa(chip, 0) != LV_OPA_COVER) {
        fprintf(stderr, "sim: chip 4's tap area is %d,%d-%d,%d, the sheet %d,%d-%d,%d\n", (int)a.x1, (int)a.y1, (int)a.x2,
                (int)a.y2, (int)b.x1, (int)b.y1, (int)b.x2, (int)b.y2);
        return fail("the 4th alert's chip, opened, isn't selected in view");
    }
    ui_sheet_close();
    run(100);
    return 0;
}

// The advisory badge's ⚠ is sized to its 12 px text: a 12 px symbol, 13 px of ink over 9 px capitals, so at most 4 px
// taller, as the warning badge's 16 px ⚠ is over its capitals. (The 16 px one it had is 16 px: 7 taller.)
static int badge_symbol(void)
{
    lv_display_trigger_activity(lv_display_get_default()); // no idle tour
    load_weather("weather.json");
    wx.alerts[0] = wx.alerts[1]; // the advisory
    wx.nalerts = 1;
    wx_seq++;
    run(300);
    tap("OK"); // notice_on_top()'s pop-up would cover the badge (the takeover notice has gone)
    lv_obj_t *l = find(lv_screen_active(), LV_SYMBOL_WARNING " WIND ADVISORY", true);
    if (!l) {
        return fail("no advisory badge");
    }
    lv_refr_now(NULL);
    lv_area_t a, bb; // the label's columns, the whole badge's rows (a glyph may poke out of its line)
    lv_obj_get_coords(l, &a);
    lv_obj_get_coords(lv_obj_get_parent(l), &bb);
    int top[2] = {H, H}, bot[2] = {-1, -1}, part = -1; // [0] the symbol's ink, [1] the text's
    for (int x = a.x1; x <= a.x2; x++) {
        bool ink = false;
        for (int y = bb.y1; y <= bb.y2; y++) {
            uint32_t p = fb[y * W + x];
            int d = 0;
            for (int s = 0; s < 24; s += 8) {
                d += abs((int)(p >> s & 0xff) - (int)(WX_ADVISORY >> s & 0xff));
            }
            if (d > 64) {
                part = part < 0 ? 0 : part;
                top[part] = LV_MIN(top[part], y);
                bot[part] = LV_MAX(bot[part], y);
                ink = true;
            }
        }
        part = !ink && part == 0 ? 1 : part; // the first empty column ends the symbol
    }
    if (bot[0] < 0 || bot[1] < 0 || bot[0] - top[0] > bot[1] - top[1] + 4) {
        fprintf(stderr, "sim: the badge's symbol is %d px tall, its text %d\n", bot[0] - top[0] + 1, bot[1] - top[1] + 1);
        return fail("the advisory badge's \xe2\x9a\xa0 is bigger than its text");
    }
    return 0;
}

// --startup: the startup screen from power-on to the first tab, a shot per state.
static int startup_walk(void)
{
    lv_obj_t *scr = lv_screen_active();
    run(600); // the fade-in runs from 300 to 900 ms
    shot("startup-fade");
    run(600);
    shot("startup-starting");
    if (!find(scr, "Starting…", false)) {
        return fail("startup: no \"Starting…\"");
    }
    wifi_state = NET_CONNECTING;
    run(600);
    shot("startup-wifi");
    wifi_state = NET_UP;
    run(600);
    shot("startup-aggregator");
    if (!find(scr, "Waiting for the aggregator…", false)) {
        return fail("startup: not waiting for the aggregator with Wi-Fi up");
    }
    lay_status = 404;
    run(600);
    shot("startup-404");
    lay_status = 0;
    agg_quiet = false;
    doc_seq++;
    run(1500); // well past the 2 s hold: with no layout it stays
    tap("OK"); // the newest note's pop-up, over the startup screen
    shot("startup-layout");
    if (!find(scr, "Loading your layout…", false)) {
        return fail("startup: gone, or not loading, with no layout yet");
    }
    load_layout("default.json");
    lv_obj_t *splash = lv_obj_get_child(scr, -1); // ui.c's next tick takes the layout, then the two 250 ms fades
    for (int i = 0; i < 60 && lv_obj_get_style_opa(splash, 0) > 150; i++) { // (the logo fades into the dark first)
        run(25);
    }
    shot("startup-fade-out");
    run(400);
    shot("startup-done");
    return find(scr, "Loading your layout…", false) ? fail("startup: still up with a layout on screen") : 0;
}

int main(int argc, char **argv)
{
    const char *mode = argc > 1 && !strncmp(argv[1], "--", 2) ? argv[1] : "";
    bool walk_layouts = !strcmp(mode, "--layouts"), rebuilt = !strcmp(mode, "--rebuilt"),
         noradar = !strcmp(mode, "--no-radar"), nowifi = !strcmp(mode, "--no-wifi"), startup = !strcmp(mode, "--startup");
    argc -= mode[0] != 0;
    argv += mode[0] != 0;
    clock_back = rebuilt;
    startup_off = !startup && !nowifi; // the other walk-throughs start where the startup screen has gone
    if ((argc != 4 && argc != 5) || (mode[0] && !walk_layouts && !rebuilt && !noradar && !nowifi && !startup)) {
        fprintf(stderr, "usage: %s [--layouts | --rebuilt | --no-radar | --no-wifi | --startup] <panel.json> <layouts dir> <outdir> [<weather dir>]\n",
                argv[0]);
        return 2;
    }
    laydir = argv[2];
    outdir = argv[3];
    wxdir = argc == 5 ? argv[4] : NULL;
    static char json[16384];
    FILE *f = fopen(argv[1], "rb");
    size_t n = f ? fread(json, 1, sizeof json, f) : 0;
    if (!f || !panel_parse(json, n, &doc)) {
        fprintf(stderr, "sim: can't read a panel document from %s\n", argv[1]);
        return 1;
    }
    fclose(f);
    if (wxdir) {
        load_weather("weather.json");
    }
    wifi_unset = nowifi;
    if (!walk_layouts && !nowifi && !startup) {
        load_layout(noradar ? "noradar.json" : "default.json"); // as if the aggregator answered at once
    }
    if (startup) {
        wifi_state = NET_OFF;
        agg_quiet = true;
    }

    load_cards("cards.json");
    load_astro("night");

    lv_init();
    lv_tick_set_cb(tick);
    lv_display_t *disp = lv_display_create(W, H);
    lv_display_set_color_format(disp, LV_COLOR_FORMAT_XRGB8888);
    lv_display_set_buffers(disp, fb, NULL, sizeof fb, LV_DISPLAY_RENDER_MODE_DIRECT);
    lv_display_set_flush_cb(disp, flush);
    lv_indev_t *touch = lv_indev_create();
    lv_indev_set_type(touch, LV_INDEV_TYPE_POINTER);
    lv_indev_set_read_cb(touch, read_finger);
    ui_init(disp);

    const char *card = NULL; // the first open card on the first board
    for (int c = 0; c < doc.boards[0].ncards && !card; c++) {
        card = doc.boards[0].cards[c].done ? NULL : doc.boards[0].cards[c].name;
    }

    if (startup) {
        return startup_walk();
    }
    if (rebuilt) { // the board and radar cards go (ui.c's tick at 500 ms) and come back (1000 ms) after the
        run(300);  // radar took its frames (100 ms), all in the plain tour's first 1.5 s: the clock stays the same
        load_layout("noradar.json");
        run(300);
        load_layout("default.json");
        run(900);
    } else {
        run(1500);
    }
    if (walk_layouts) {
        return layouts();
    }
    if (nowifi) {
        shot("first-boot");
        return find(lv_screen_active(), "Wi-Fi isn't set up yet.", true) ? 0 : fail("no set-up note with Wi-Fi off");
    }
    if (noradar) {
        return no_radar();
    }
    if (!wxdir) { // weather isn't set up: the Weather tab says so; then the astronomical clock
        tap("OK");
        tap("Weather");
        shot("weather-unset");
        return astro();
    }
    shot("notification"); // the newest unread note pops up over the Homelab page
    tap("OK");
    shot("homelab");
    tap("NAS ALERTS");
    shot("alerts");
    tap("Close");
    tap_at(LV_SYMBOL_BELL, true);
    shot("history");
    tap("Close");
    tap("Planka");
    shot("planka");
    if (card) {
        tap(card);
        shot("done-sheet");
        tap(LV_SYMBOL_OK "  Done");
        shot("done");
    }
    run(3500);      // until the "Marked done" toast has gone
    tap("Weather"); // the radar loop starts from the oldest frame
    run(200);
    shot("weather-radar");
    lv_obj_t *home = find(lv_screen_active(), "home", false); // its parent is the marker, whose child 0 the ring
    lv_obj_t *ring = home ? lv_obj_get_child(lv_obj_get_parent(home), 0) : NULL;
    if (!ring || lv_obj_get_width(ring) != ellipse_w(28) || lv_obj_get_height(ring) != 28) {
        fprintf(stderr, "sim: the radar's home ring is %d x %d, want %d x 28\n", ring ? (int)lv_obj_get_width(ring) : 0,
                ring ? (int)lv_obj_get_height(ring) : 0, ellipse_w(28));
        return 1;
    }
    run(13000); // 3 loops of 5 frames, then the map dims under the conditions
    shot("weather-conditions");
    lv_obj_t *hum = find(lv_screen_active(), "Humidity", true); // in the text beside the icon, in the overlay
    lv_obj_t *icon = hum ? lv_obj_get_child(lv_obj_get_parent(lv_obj_get_parent(hum)), 0) : NULL;
    int discs = 0;
    for (uint32_t i = 0; icon && i < lv_obj_get_child_count(icon); i++) {
        lv_obj_t *o = lv_obj_get_child(icon, (int32_t)i);
        int w = lv_obj_get_width(o), h = lv_obj_get_height(o);
        if (lv_obj_get_style_radius(o, LV_PART_MAIN) != LV_RADIUS_CIRCLE) {
            continue;
        }
        discs++;
        if (abs(w - ellipse_w(h)) > 1) {
            fprintf(stderr, "sim: a disc of the weather icon is %d x %d, want %d \xc2\xb1 1 wide\n", w, h, ellipse_w(h));
            return 1;
        }
    }
    if (!discs) {
        fprintf(stderr, "sim: the weather icon has no discs\n");
        return 1;
    }
    lv_obj_t *cond = find(lv_screen_active(), "Partly cloudy", false), *hilo = find(lv_screen_active(), "H ", true);
    if (!cond || lv_obj_get_style_text_font(cond, 0) != font_text_22 || !hilo ||
        lv_obj_get_style_text_font(hilo, 0) != font_text_17) {
        return fail("the conditions' text isn't the mockup's 22 / 17 px");
    }
    load_weather("warning.json"); // a tornado warning covers home: back to the radar, red frame, sticky notice
    run(1500); // past the fade back to the radar, the badge mid-pulse
    shot("weather-warning");
    lv_obj_t *nws = find(lv_layer_top(), "National Weather Service", true);
    lv_obj_t *credit = find(lv_screen_active(), "\xc2\xa9 OpenStreetMap contributors", false);
    lv_area_t na, ca;
    if (!nws || !credit) {
        return fail("no takeover notice or no map credit");
    }
    lv_obj_get_coords(lv_obj_get_parent(lv_obj_get_parent(nws)), &na);
    lv_obj_get_coords(credit, &ca);
    if (na.x1 <= ca.x2 && ca.x1 <= na.x2 && na.y1 <= ca.y2 && ca.y1 <= na.y2) {
        return fail("the takeover notice covers the map credit");
    }
    lv_obj_t *tchip = find(lv_screen_active(), "\xe2\x88\x92", true); // "−N min · 2:30 PM", or "now · 2:45 PM"
    tchip = tchip ? tchip : find(lv_screen_active(), "now \xc2\xb7", true);
    lv_obj_t *nmsg = find(lv_layer_top(), "Tornado Warning for your area", true);
    if (!tchip || lv_obj_get_style_text_font(tchip, 0) != font_text_14 || !nmsg ||
        lv_obj_get_style_text_font(nmsg, 0) != font_text_17 || lv_obj_get_style_text_font(nws, 0) != font_text_14) {
        return fail("the time chip or the notice's text isn't the mockup's 14 / 17 / 14 px");
    }
    tap_at(LV_SYMBOL_WARNING, true); // the badge
    shot("weather-alert-sheet");
    lv_obj_t *hl = find(lv_layer_top(), "Tornado Warning issued", true), *when = find(lv_layer_top(), "Issued ", true);
    if (!hl || lv_obj_get_style_text_font(hl, 0) != font_text_20 || !when ||
        lv_obj_get_style_text_font(when, 0) != font_text_14 || !find(lv_layer_top(), "#ffd35a HAZARD", true)) {
        return fail("the alert sheet's headline, times or keyword isn't the mockup's");
    }
    lv_obj_scroll_to_y(lv_obj_get_parent(hl), 100000, LV_ANIM_OFF); // the instruction, at the end of the text
    lv_obj_t *act = find(lv_layer_top(), "TAKE COVER NOW!", true);
    act = act ? lv_obj_get_parent(act) : NULL;
    if (!act || lv_obj_get_style_border_side(act, 0) != LV_BORDER_SIDE_LEFT || lv_obj_get_style_border_width(act, 0) != 4 ||
        !lv_color_eq(lv_obj_get_style_border_color(act, 0), lv_color_hex(WX_RED))) {
        return fail("the alert sheet's instruction isn't in a red-edged block");
    }
    lv_area_t c; // a chip answers a tap just above it too: its tap area is 60+ px tall
    lv_obj_get_coords(lv_obj_get_parent(find(lv_layer_top(), "Tornado Watch", false)), &c);
    press(c.x1 + 20, c.y1 - 10);
    if (!find(lv_layer_top(), "Tornado Watch issued", true)) {
        fprintf(stderr, "sim: a tap just above the watch chip didn't select it\n");
        return 1;
    }
    tap("Close");
    tap("OK"); // only the notice's own OK dismisses it, and a later document doesn't bring it back
    load_weather("warning.json");
    run(500);
    if (find(lv_layer_top(), "National Weather Service", true)) {
        fprintf(stderr, "sim: the takeover notice is still up after OK\n");
        return 1;
    }
    wx.alerts[0].lvl = 1; // ...but an escalation does, worded so it can't pass for the one OK'd
    wx_seq++;
    run(500);
    lv_obj_t *m = find(lv_layer_top(), "Tornado Warning for your area", true);
    if (!m || !strstr(lv_label_get_text(m), " \xE2\x80\x94 tornado observed. Take shelter now.") ||
        !find(lv_screen_active(), LV_SYMBOL_WARNING " TORNADO OBSERVED", true)) {
        fprintf(stderr, "sim: the escalated warning didn't come back as \"tornado observed\"\n");
        return 1;
    }
    return clock_over_radar() || tour() || loop_states() || notice_on_top() || sheet_four() || badge_symbol();
}
