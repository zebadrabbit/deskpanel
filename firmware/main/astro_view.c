// The astronomical clock (spec "Design -> Look", mockup astro-ring-v3.html): a night-sky dial centred on
// (240, 240) and a column at x 500..770, full screen as the screen's last child (A7). The panel's pixels are wider
// than tall, so everything round on the dial is PANEL_ASPECT narrower (radii below are physical: as tall as they
// are, 1/PANEL_ASPECT as wide), to be round on the glass. Every object is made once
// in astro_view_init(); after that only guarded setters run, so an unchanged value costs no redraw (redraw load
// makes the RGB panel drift): the hands move once a minute, the moon ring refills when the phase changes, the
// day/night ring when the times change, the sky when a new picture arrives, the column with a new document.
#include "astro_view.h"

#include <math.h>
#include <stdio.h>
#include <string.h>
#include <time.h>

#include "ellipse.h"
#include "esp_attr.h"
#include "net.h"
#include "page.h"

// sky_dsc points at the net layer's front buffer and fill_dsc at fill_px, both changed in place: like
// card_radar.c this needs LVGL's image cache off (a cached entry would keep showing the old pixels).
#if LV_CACHE_DEF_SIZE
#error "astro_view.c changes image data in place: drop the cached entry after each change"
#endif

#define CX       240 // the dial's centre; the view is the whole screen at (0, 0), so these are screen pixels
#define CY       240
#define SKY_R    189
#define STALE_MS (10 * 60 * 1000) // the sky greys after this long without a good poll
#define FADE_MS  300

// #03050c as the panel's RGB565 holds it (0x0021): the same there, and in the full-colour sim it matches the sky
// picture's painted corners.
#define C_SCREEN 0x000408
#define C_DIAL   0x040814 // also the sky picture's corners (A4)
#define C_SKY    0x08132f // the plain sky before a picture (and with no location): the night gradient's average
#define C_GOLD   0xc9a75e // ticks, hour numbers, rim, compass letters
#define C_DAY    0xd8b56a // the daylight arc, the column's icons and heading
#define C_NIGHT  0x1b2c55
#define C_HAND   0xe2c27a
#define C_MOON   0xefe3bd
#define C_RING   0x0a1226 // inside the hour hand's ring

#define COL_X   500 // the column spans x 500..770
#define COL_W   271
#define ROWS_Y  124 // the rows box: everything under the first divider, hidden with no location
#define ROW_Y   148 // the six rows' baselines, 31 px apart
#define ROW_DY  31
#define NAME_W  138 // x 528..665
#define VALUE_W 102 // x 668..769, right-aligned
#define OVER_W  268 // x 502..769
#define FILL    24  // the moon ring's fill, radius 12: FILL tall, fill_w (the ring's inside) wide
#define RING_R  222.5f // the day/night ring's middle: r 220..225 (the mockup's r 222, 5 px)
#define RIM_R   189.5f // the gold rim's: r 188..191

extern const uint8_t ttf_start[] asm("_binary_Montserrat_Medium_ttf_start");
extern const uint8_t ttf_end[] asm("_binary_Montserrat_Medium_ttf_end");
extern const uint8_t semi_start[] asm("_binary_Montserrat_SemiBold_sub_ttf_start");
extern const uint8_t semi_end[] asm("_binary_Montserrat_SemiBold_sub_ttf_end");

static const lv_font_t *f72, *f15, *f11, *f14; // its 22 and 17 px text: ui.c's font_text_22 / _17
static lv_obj_t *view, *dial, *sky_base, *sky, *hands_box, *fill;
// The hand pieces, each twice: [0] only casts the shadow, [1] is drawn over every shadow at the same geometry.
static lv_obj_t *minute[2], *hour[2], *point[2], *ring[2];
static lv_obj_t *covered[8]; // the screen's other children the open view hid (LVGL would keep redrawing them)
static int ncovered;
static lv_obj_t *t_time, *t_ampm, *t_date, *rows, *names[6], *values[6], *heading, *over[4], *nolocation;
static lv_point_precise_t pts[3][2]; // the hand lines' points (lv_line keeps the pointer)
static lv_image_dsc_t sky_dsc, fill_dsc;
EXT_RAM_BSS_ATTR static uint32_t fill_px[FILL * FILL]; // ARGB8888, fill_w x FILL used
static int fill_w;
// The rings are ellipses (ellipse.h): arcs of circles fitted to them, centred on the dial's centre.
static ellipse_arc_t ring_arcs[ELLIPSE_ARCS], rim_arcs[ELLIPSE_ARCS];
// The day/night ring: pieces of ring_arcs, by day_night().
static ellipse_arc_t night_arcs[ELLIPSE_ARCS + 1], day_arcs[ELLIPSE_ARCS + 1];
static int nnight, nday;
static astro_t astro = {.illum = -1, .day0 = -1, .day1 = -1}; // the document shown ("—" until the first)
static uint32_t astro_seen, sky_seen;
static bool opened, have_sky, located = true;
static int day0 = -1, day1 = -1; // the day/night ring as drawn: sunrise, sunset (-1: all night)
static long long shown_min = -1;

// The point at physical radius r from the dial's centre, `deg` clockwise from straight up.
static lv_point_t polar(float r, float deg)
{
    float a = deg * (float)M_PI / 180;
    return (lv_point_t){CX + (int32_t)lroundf(r * sinf(a) / PANEL_ASPECT), CY - (int32_t)lroundf(r * cosf(a))};
}

static float day_deg(int minute_of_day) { return minute_of_day / 4.0f + 180; } // 24 hours: noon up, 06 left

// The day/night ring from day0, day1: the daylight arc from sunrise to sunset, the night the rest, meeting there.
// Neither is drawn over the other.
static void day_night(void)
{
    if (day0 < 0 || day1 < 0) { // polar day or night, or no data: all night
        memcpy(night_arcs, ring_arcs, sizeof ring_arcs);
        nnight = ELLIPSE_ARCS;
        nday = 0;
        return;
    }
    float a = day_deg(day0), span = fmodf(day_deg(day1) - a + 360, 360);
    nday = ellipse_cut(day_arcs, ring_arcs, RING_R, a, span);
    nnight = ellipse_cut(night_arcs, ring_arcs, RING_R, a + span, 360 - span);
}

static lv_obj_t *plain(lv_obj_t *parent, int x, int y, int w, int h)
{
    lv_obj_t *o = lv_obj_create(parent);
    lv_obj_remove_style_all(o);
    lv_obj_set_scrollable(o, false);
    lv_obj_set_clickable(o, false); // every tap lands on the view itself
    lv_obj_set_pos(o, x, y);
    lv_obj_set_size(o, w, h);
    return o;
}

// A disc of radius r, round on the glass: ellipse_w(2r) x 2r, a stadium (LVGL has no ellipse), so between the axes it
// reaches up to 1.4 % past the ellipse.
static lv_obj_t *disc(lv_obj_t *parent, int cx, int cy, int r, uint32_t color)
{
    int w = ellipse_w(2 * r);
    lv_obj_t *o = plain(parent, cx - w / 2, cy - r, w, 2 * r);
    lv_obj_set_style_radius(o, LV_RADIUS_CIRCLE, 0);
    lv_obj_set_style_bg_color(o, lv_color_hex(color), 0);
    lv_obj_set_style_bg_opa(o, LV_OPA_COVER, 0);
    return o;
}

static void shadow_extent(lv_event_t *e) // the blur reaches past LVGL's own redraw margin (~11 px)
{
    int32_t *s = lv_event_get_param(e);
    *s = LV_MAX(*s, 16);
}

static void shadow(lv_obj_t *o) // the hands' soft drop shadow, down-right as if lit from the top left
{
    lv_obj_set_style_drop_shadow_opa(o, 191, 0);
    lv_obj_set_style_drop_shadow_radius(o, 6, 0);
    lv_obj_set_style_drop_shadow_offset_x(o, 3, 0);
    lv_obj_set_style_drop_shadow_offset_y(o, 4, 0);
    lv_obj_add_event_cb(o, shadow_extent, LV_EVENT_REFR_EXT_DRAW_SIZE, NULL);
    lv_obj_refresh_ext_draw_size(o);
}

static void show(lv_obj_t *o, bool on) // only on a change: a set redraws even when nothing changes
{
    if (lv_obj_is_hidden(o) == on) {
        lv_obj_set_hidden(o, !on);
    }
}

// A label placed by its first line's baseline; w > 0 keeps it on one line w wide, cut with "...".
static lv_obj_t *text(lv_obj_t *parent, const lv_font_t *f, uint32_t color, int x, int baseline, int w)
{
    lv_obj_t *l = ui_label(parent, f, color);
    lv_obj_set_pos(l, x, baseline - (f->line_height - f->base_line));
    if (w) {
        lv_obj_set_size(l, w, f->line_height);
        lv_label_set_long_mode(l, LV_LABEL_LONG_MODE_DOTS);
    }
    return l;
}

static lv_obj_t *centred(const lv_font_t *f, uint32_t color, const char *s, lv_point_t at) // a label centred on at
{
    lv_obj_t *l = ui_label(view, f, color);
    lv_label_set_text(l, s);
    lv_obj_align(l, LV_ALIGN_CENTER, at.x - 400, at.y - 240); // from the view's centre
    return l;
}

static void rule(lv_obj_t *parent, int x, int y) // a column divider
{
    lv_obj_t *o = plain(parent, x, y, COL_W, 1);
    lv_obj_set_style_bg_color(o, lv_color_hex(COL_DIM), 0);
    lv_obj_set_style_bg_opa(o, 115, 0);
}

// Drawn over the sky picture (astro_sky_fit() painted its square's corners outside the dial the screen's colour):
// the day/night ring (navy, the daylight arc over it in gold), the gold rim, the 24 hour ticks and N/E/S/W. The
// rings are ellipses (LVGL draws only round arcs), in colours mixed over the dial's as the mockup's opacities.
static void draw_dial(lv_event_t *e)
{
    lv_layer_t *layer = lv_event_get_layer(e);
    lv_color_t dial_c = lv_color_hex(C_DIAL);
    lv_point_t mid = {CX, CY};
    ellipse_draw(layer, night_arcs, nnight, mid, 5, lv_color_mix(lv_color_hex(C_NIGHT), dial_c, 242), false);
    ellipse_draw(layer, day_arcs, nday, mid, 5, lv_color_mix(lv_color_hex(C_DAY), dial_c, 153), false);
    ellipse_draw(layer, rim_arcs, ELLIPSE_ARCS, mid, 3, lv_color_hex(C_GOLD), false);
    lv_draw_line_dsc_t l;
    lv_draw_line_dsc_init(&l);
    l.color = lv_color_hex(C_GOLD);
    for (int h = 0; h < 24; h++) { // minors 1 px r 212..218; majors at 00/06/12/18 4 px, round, r 211..225
        bool major = h % 6 == 0;
        lv_point_t p1 = polar(major ? 211 : 212, day_deg(h * 60)), p2 = polar(major ? 225 : 218, day_deg(h * 60));
        l.p1 = (lv_point_precise_t){p1.x, p1.y};
        l.p2 = (lv_point_precise_t){p2.x, p2.y};
        l.width = major ? 4 : 1;
        l.round_start = l.round_end = major;
        l.opa = major ? 242 : 204;
        lv_draw_line(layer, &l);
    }
    // N/E/S/W just inside the rim at altitude 8° (east on the left, as you look up), each over a dark halo so it
    // reads over the stars: the letter in the dial's colour a pixel out all round, then in gold.
    static const char *const nesw[4] = {"N", "W", "S", "E"}; // clockwise from the top
    static const int8_t ofs[9][2] = {{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}, {0, 0}};
    lv_draw_label_dsc_t t;
    lv_draw_label_dsc_init(&t);
    t.font = f14;
    for (int i = 0; i < 4; i++) {
        lv_point_t c = polar(SKY_R * (90 - 8) / 90.0f, i * 90), sz;
        lv_text_get_size(&sz, nesw[i], f14, 0, 0, LV_COORD_MAX, LV_TEXT_FLAG_NONE);
        t.text = nesw[i];
        for (int k = 0; k < 9; k++) {
            t.color = lv_color_hex(k < 8 ? C_DIAL : C_GOLD);
            int32_t x = c.x - sz.x / 2 + ofs[k][0], y = c.y - sz.y / 2 + ofs[k][1];
            lv_area_t a = {x, y, x + sz.x - 1, y + sz.y - 1};
            lv_draw_label(layer, &t, &a);
        }
    }
}

static void icon_disc(lv_layer_t *layer, lv_draw_rect_dsc_t *d, int x, int y, int r, uint32_t color)
{
    d->bg_color = lv_color_hex(color);
    lv_area_t a = {x - r, y - r, x + r - 1, y + r - 1};
    lv_draw_rect(layer, d, &a);
}

// The rows' gold icons (the mockup's text glyphs, which Montserrat lacks): a sun for sunrise and sunset, a
// clock for daylight, a crescent for the moon's three rows.
static void draw_icons(lv_event_t *e)
{
    lv_layer_t *layer = lv_event_get_layer(e);
    lv_area_t box;
    lv_obj_get_coords(lv_event_get_target_obj(e), &box);
    lv_draw_rect_dsc_t d;
    lv_draw_rect_dsc_init(&d);
    d.radius = LV_RADIUS_CIRCLE;
    lv_draw_line_dsc_t l;
    lv_draw_line_dsc_init(&l);
    l.color = lv_color_hex(C_DAY);
    l.width = 1;
    lv_draw_arc_dsc_t a;
    lv_draw_arc_dsc_init(&a);
    a.color = l.color;
    a.width = 1;
    a.radius = 6;
    a.end_angle = 360;
    for (int i = 0; i < 6; i++) {
        int x = box.x1 + 7, y = box.y1 + ROW_Y - ROWS_Y + i * ROW_DY - 5;
        if (i < 2) {
            icon_disc(layer, &d, x, y, 3, C_DAY);
            for (int k = 0; k < 8; k++) {
                float s = sinf(k * (float)M_PI / 4), c = cosf(k * (float)M_PI / 4);
                l.p1 = (lv_point_precise_t){x + (int32_t)lroundf(4.5f * s), y - (int32_t)lroundf(4.5f * c)};
                l.p2 = (lv_point_precise_t){x + (int32_t)lroundf(6.5f * s), y - (int32_t)lroundf(6.5f * c)};
                lv_draw_line(layer, &l);
            }
        } else if (i == 2) {
            a.center = (lv_point_t){x, y};
            lv_draw_arc(layer, &a);
            l.p1 = (lv_point_precise_t){x, y};
            l.p2 = (lv_point_precise_t){x, y - 4};
            lv_draw_line(layer, &l);
            l.p2 = (lv_point_precise_t){x + 3, y};
            lv_draw_line(layer, &l);
        } else {
            icon_disc(layer, &d, x, y, 6, C_DAY);
            icon_disc(layer, &d, x + 3, y - 1, 6, C_SCREEN);
        }
    }
}

static lv_obj_t *hand_line(int width, bool caster)
{
    lv_obj_t *l = lv_line_create(hands_box);
    lv_obj_remove_style_all(l);
    lv_obj_set_style_line_width(l, width, 0);
    lv_obj_set_style_line_color(l, lv_color_hex(C_HAND), 0);
    lv_obj_set_style_line_rounded(l, true, 0);
    if (caster) {
        shadow(l);
    }
    return l;
}

static lv_obj_t *hand_ring(bool caster) // r 12..16 (the mockup's r 14, 4.4 px), dark inside
{
    lv_obj_t *o = plain(hands_box, 0, 0, ellipse_w(32), 32);
    lv_obj_set_style_radius(o, LV_RADIUS_CIRCLE, 0);
    lv_obj_set_style_bg_color(o, lv_color_hex(C_RING), 0);
    lv_obj_set_style_bg_opa(o, LV_OPA_COVER, 0);
    lv_obj_set_style_border_width(o, 4, 0);
    lv_obj_set_style_border_color(o, lv_color_hex(C_HAND), 0);
    if (caster) {
        shadow(o);
    }
    return o;
}

// Lays line l (points p) from radius r0 to r1 at deg, placed at its own bounding box so a move redraws only that.
static void aim(lv_obj_t *l, lv_point_precise_t *p, float r0, float r1, float deg)
{
    lv_point_t a = polar(r0, deg), b = polar(r1, deg);
    int32_t x = LV_MIN(a.x, b.x), y = LV_MIN(a.y, b.y);
    p[0] = (lv_point_precise_t){a.x - x, a.y - y};
    p[1] = (lv_point_precise_t){b.x - x, b.y - y};
    lv_obj_set_pos(l, x, y);
    lv_line_set_points(l, p, 2);
}

// Minute: a baton from the tail (r -24) to r 176. Hour, on the 24-hour dial: a baton to r 104, the ring
// centred at r 118 (radius 14), a point from r 132 to r 142.
static void hands(const struct tm *tm)
{
    float h = day_deg(tm->tm_hour * 60 + tm->tm_min);
    lv_point_t c = polar(118, h);
    for (int k = 0; k < 2; k++) { // the shadow casters and the pieces share their points
        aim(minute[k], pts[0], -24, 176, tm->tm_min * 6.0f);
        aim(hour[k], pts[1], -20, 104, h);
        aim(point[k], pts[2], 132, 142, h);
        lv_obj_set_pos(ring[k], c.x - ellipse_w(32) / 2, c.y - 16);
    }
}

// The ring's inside, lit #efe3bd to the phase over the ring's dark fill: the terminator is a half-ellipse, so
// the lit area is exactly that share of the disc. Waxing is lit on the right. 4 x 4 samples a pixel. Redrawn
// only when the whole percent (the column's "% lit") or waxing changes: every hour or two at most.
static void moon_fill(float illum, bool waxing) // illum < 0: unknown, left dark
{
    static int shown = -2;
    static bool shown_wax;
    int pct = illum < 0 ? -1 : (int)lroundf(illum * 100);
    if (pct == shown && waxing == shown_wax) {
        return;
    }
    shown = pct;
    shown_wax = waxing;
    for (int j = 0; j < FILL; j++) {
        for (int i = 0; i < fill_w; i++) { // u, v: -1..1 across the (narrowed) disc
            int lit = 0;
            for (int s = 0; pct >= 0 && s < 16; s++) {
                float u = (i + (s % 4 + 0.5f) / 4) * 2 / fill_w - 1, v = (j + (s / 4 + 0.5f) / 4) * 2 / FILL - 1;
                lit += u * u + v * v <= 1 && (waxing ? u : -u) >= (1 - pct / 50.0f) * sqrtf(1 - v * v);
            }
            fill_px[j * fill_w + i] = (uint32_t)(lit * 255 / 16) << 24 | C_MOON;
        }
    }
    lv_obj_invalidate(fill);
}

// The overhead names first..first+n-1 joined with " · " into buf (plus "  +more" when more > 0); its width.
static int join(char *buf, size_t cap, int first, int n, int more)
{
    int len = 0;
    buf[0] = '\0';
    for (int i = first; i < first + n; i++) {
        len += snprintf(buf + len, cap - len, "%s%s", i > first ? " \xc2\xb7 " : "", astro.over[i]);
    }
    if (more) {
        snprintf(buf + len, cap - len, "  +%d", more);
    }
    lv_point_t sz;
    lv_text_get_size(&sz, buf, f15, 0, 0, LV_COORD_MAX, LV_TEXT_FLAG_NONE);
    return sz.x;
}

// Up to 4 lines, as many names on each as fit; what's left becomes "+N" on the 4th (which gives up names
// until that fits).
static void overhead(void)
{
    char buf[ASTRO_OVER_MAX * 28 + 8];
    int first = 0;
    for (int k = 0; k < 4; k++) {
        int n = 0;
        while (first + n < astro.nover && (n == 0 || join(buf, sizeof buf, first, n + 1, 0) <= OVER_W)) {
            n++;
        }
        int more = k == 3 ? astro.nover - first - n : 0;
        while (more && n > 1 && join(buf, sizeof buf, first, n, more) > OVER_W) {
            n--, more++;
        }
        join(buf, sizeof buf, first, n, more);
        ui_text(over[k], buf);
        first += n;
    }
}

static const char *or_dash(const char *s) { return s[0] ? s : "\xe2\x80\x94"; }

static void column(void)
{
    static const char *const label[6] = {"Sunrise", "Sunset", "Daylight", NULL, "Moonrise", "Moonset"};
    char lit[16] = "";
    if (astro.illum >= 0) {
        snprintf(lit, sizeof lit, "%d %% lit", (int)lroundf(astro.illum * 100));
    }
    const char *val[6] = {astro.rise, astro.set, astro.daylight, lit, astro.moonrise, astro.moonset};
    for (int i = 0; i < 6; i++) {
        ui_text(names[i], label[i] ? label[i] : or_dash(astro.phase));
        ui_text(values[i], or_dash(val[i]));
    }
    ui_text(heading, astro.label[0] ? astro.label : "OVERHEAD NOW");
    overhead();
}

// The time and date from the panel's own clock (as the status bar), so they run with no server.
static void clock_text(void)
{
    char t[16] = "--:--", ap[8] = "", d[48] = "";
    time_t now = time(NULL);
    struct tm tm;
    localtime_r(&now, &tm);
    bool ok = net_status().time_ok;
    show(hands_box, ok); // no hands at a 1970 time
    if (ok) {
        strftime(t, sizeof t, "%I:%M", &tm);
        strftime(ap, sizeof ap, "%p", &tm);
        strftime(d, sizeof d, "%A, %B ", &tm);
        snprintf(d + strlen(d), sizeof d - strlen(d), "%d", tm.tm_mday); // no leading zero or space
    }
    const char *tt = t[0] == '0' ? t + 1 : t;
    if (strcmp(lv_label_get_text(t_time), tt)) {
        lv_label_set_text(t_time, tt);
        lv_point_t sz;
        lv_text_get_size(&sz, tt, f72, 0, 0, LV_COORD_MAX, LV_TEXT_FLAG_NONE);
        lv_obj_set_x(t_ampm, COL_X + sz.x + 6);
    }
    ui_text(t_ampm, ap);
    ui_text(t_date, d);
    if (now / 60 != shown_min) {
        shown_min = now / 60;
        hands(&tm);
    }
}

void astro_view_tick(void)
{
    const uint16_t *px;
    long long t;
    if (net_sky_take(&px, &t, &sky_seen)) { // switch now: the net task may reuse the old buffer next
        sky_dsc.data = (const uint8_t *)px;
        lv_image_set_src(sky, &sky_dsc);
        have_sky = true;
    }
    bool took = net_astro_take(&astro, &astro_seen), was = located;
    located = net_astro_status() != 404; // A3: no location: no sky, no rows, nothing of the old document's on the dial
    if (took || located != was) {
        int d0 = located ? astro.day0 : -1, d1 = located ? astro.day1 : -1;
        if (d0 != day0 || d1 != day1) {
            day0 = d0;
            day1 = d1;
            day_night();
            lv_obj_invalidate(dial);
        }
        moon_fill(located ? astro.illum : -1, astro.waxing);
    }
    if (took) {
        column();
    }
    show(rows, located);
    show(nolocation, !located);
    show(sky, have_sky && located);
    show(sky_base, !(have_sky && located));
    int64_t age = net_astro_age_ms(); // -1: no good poll since boot
    bool stale = astro.stale || (age < 0 ? lv_tick_get() > STALE_MS : age > STALE_MS);
    lv_opa_t opa = stale ? LV_OPA_60 : LV_OPA_COVER;
    if (lv_obj_get_style_image_opa(sky, 0) != opa) {
        lv_obj_set_style_image_opa(sky, opa, 0);
    }
    clock_text();
}

static void set_opa(void *o, int32_t v) { lv_obj_set_style_opa(o, (lv_opa_t)v, 0); }

static void fade(lv_opa_t from, lv_opa_t to, lv_anim_completed_cb_t done)
{
    lv_anim_t a;
    lv_anim_init(&a);
    lv_anim_set_var(&a, view);
    lv_anim_set_exec_cb(&a, set_opa);
    lv_anim_set_values(&a, from, to);
    lv_anim_set_duration(&a, FADE_MS);
    lv_anim_set_completed_cb(&a, done);
    lv_anim_start(&a);
}

static void hide(lv_anim_t *a)
{
    LV_UNUSED(a);
    lv_obj_set_hidden(view, true);
    set_opa(view, LV_OPA_COVER);
}

// Once the view is fully open, hide the screen's other children (the status bar, the tabs): LVGL doesn't skip an
// object another covers, so their own updates (the radar loop, the board's paging, the RSSI) would keep
// repainting the clock. uncover() shows exactly those again.
static void cover(lv_anim_t *a)
{
    LV_UNUSED(a);
    static bool logged;
    if (!logged) {
        logged = true;
        net_log_heap("heap after first clock open");
    }
    lv_obj_t *scr = lv_obj_get_parent(view);
    for (uint32_t i = 0; i < lv_obj_get_child_count(scr) && ncovered < 8; i++) {
        lv_obj_t *c = lv_obj_get_child(scr, (int32_t)i);
        if (c != view && !lv_obj_is_hidden(c)) {
            lv_obj_set_hidden(c, true);
            covered[ncovered++] = c;
        }
    }
}

static void uncover(void)
{
    while (ncovered) {
        lv_obj_set_hidden(covered[--ncovered], false);
    }
}

void astro_view_open(bool animate)
{
    bool was_hidden = lv_obj_is_hidden(view);
    opened = true;
    lv_anim_delete(view, set_opa);
    lv_obj_set_hidden(view, false);
    if (animate) {
        fade(was_hidden ? LV_OPA_TRANSP : lv_obj_get_style_opa(view, 0), LV_OPA_COVER, cover);
    } else {
        set_opa(view, LV_OPA_COVER);
        cover(NULL);
    }
}

void astro_view_close(bool animate)
{
    opened = false;
    uncover(); // first: a fade-out shows them through
    lv_anim_delete(view, set_opa);
    if (animate && !lv_obj_is_hidden(view)) {
        fade(lv_obj_get_style_opa(view, 0), LV_OPA_TRANSP, hide);
    } else {
        hide(NULL);
    }
}

bool astro_view_is_open(void) { return opened; }

// A tap closes it, and only that: the view takes the whole touch. (During the idle tour ui.c's catcher, over the
// view, takes the tap instead.)
static void on_tap(lv_event_t *e) { astro_view_close(true); }

void astro_view_init(lv_obj_t *screen)
{
    size_t medium = ttf_end - ttf_start;
    f72 = lv_tiny_ttf_create_data_ex(ttf_start, medium, 72, LV_FONT_KERNING_NORMAL, 6); // ~2 KB a glyph: the time's
    f15 = lv_tiny_ttf_create_data(ttf_start, medium, 15);
    f11 = lv_tiny_ttf_create_data(ttf_start, medium, 11);
    f14 = lv_tiny_ttf_create_data(semi_start, semi_end - semi_start, 14);

    view = plain(screen, 0, 0, LV_PCT(100), LV_PCT(100));
    lv_obj_set_clickable(view, true); // takes every tap, so nothing underneath is pressed
    lv_obj_add_event_cb(view, on_tap, LV_EVENT_CLICKED, NULL);
    lv_obj_set_style_bg_color(view, lv_color_hex(C_SCREEN), 0);
    lv_obj_set_style_bg_opa(view, LV_OPA_COVER, 0);
    lv_obj_set_hidden(view, true);

    // The dial: disc, the sky, then over it draw_dial() (day/night ring, gold rim, ticks, N/E/S/W) and the hour
    // numbers (noon up, 06 left).
    disc(view, CX, CY, DIAL_R, C_DIAL);
    // 2 px in: the stadium reaches 2.6 px past the sky's ellipse between the axes, and must stay under the rim.
    sky_base = disc(view, CX, CY, SKY_R - 2, C_SKY);
    sky_dsc.header.magic = LV_IMAGE_HEADER_MAGIC;
    sky_dsc.header.cf = LV_COLOR_FORMAT_RGB565;
    sky_dsc.header.w = SKY_FIT_W; // astro_sky_fit() narrowed each row in place; the rows keep their stride
    sky_dsc.header.h = SKY_H;
    sky_dsc.header.stride = SKY_W * 2;
    sky_dsc.data_size = SKY_BYTES;
    sky = lv_image_create(view);
    lv_obj_set_pos(sky, CX - SKY_FIT_W / 2, CY - SKY_R);
    lv_obj_set_hidden(sky, true);
    ellipse_fit(ring_arcs, RING_R, 5);
    ellipse_fit(rim_arcs, RIM_R, 3);
    day_night();
    dial = plain(view, CX - DIAL_R, CY - DIAL_R, 2 * DIAL_R, 2 * DIAL_R);
    lv_obj_add_event_cb(dial, draw_dial, LV_EVENT_DRAW_MAIN, NULL);
    static const char *const hrs[4] = {"00", "06", "12", "18"};
    for (int i = 0; i < 4; i++) {
        lv_obj_set_style_text_opa(centred(f11, C_GOLD, hrs[i], polar(199, day_deg(i * 360))), 217, 0);
    }

    // The hands, moved by hands(): minute under hour, then the centre cap. As the mockup's one shadow under the
    // group, every shadow lies under every piece: the shadow-casting copies come first, the pieces over them.
    hands_box = plain(view, 0, 0, 2 * CX, 2 * CY); // hidden while the clock isn't set
    for (int k = 0; k < 2; k++) {
        minute[k] = hand_line(4, !k);
        hour[k] = hand_line(7, !k);
        ring[k] = hand_ring(!k);
        point[k] = hand_line(4, !k);
        lv_obj_t *cap = disc(hands_box, CX, CY, 9, C_HAND);
        if (!k) {
            shadow(cap);
        }
    }
    fill_w = ellipse_w(32) - 2 * 4; // the hand ring's inside: its width less the border
    fill_dsc.header.magic = LV_IMAGE_HEADER_MAGIC;
    fill_dsc.header.cf = LV_COLOR_FORMAT_ARGB8888;
    fill_dsc.header.w = fill_w;
    fill_dsc.header.h = FILL;
    fill_dsc.header.stride = fill_w * 4;
    fill_dsc.data = (const uint8_t *)fill_px;
    fill_dsc.data_size = fill_w * FILL * 4;
    fill = lv_image_create(ring[1]); // inside the border, upright whatever the hand's angle
    lv_image_set_src(fill, &fill_dsc);
    lv_obj_center(fill);
    disc(hands_box, CX, CY, 3, C_SCREEN);

    // The column (Montserrat): time 72 px with AM/PM 22 px dim, date 17 px dim, a divider, then the rows box:
    // six rows (gold icon, dim label, right-aligned value), a divider, the heading and up to 4 overhead lines.
    t_time = text(view, f72, COL_TEXT, COL_X, 74, 0);
    t_ampm = text(view, font_text_22, COL_DIM, COL_X, 74, 0);
    t_date = text(view, font_text_17, COL_DIM, COL_X + 2, 102, 0);
    rule(view, COL_X, 120);
    rows = plain(view, COL_X, ROWS_Y, COL_W, 480 - ROWS_Y);
    lv_obj_add_event_cb(plain(rows, 0, 0, 16, 6 * ROW_DY), draw_icons, LV_EVENT_DRAW_MAIN, NULL);
    for (int i = 0; i < 6; i++) {
        int y = ROW_Y - ROWS_Y + i * ROW_DY;
        names[i] = text(rows, f15, COL_DIM, 28, y, NAME_W);
        values[i] = text(rows, font_text_16, COL_TEXT, COL_W - VALUE_W, y, VALUE_W);
        lv_obj_set_style_text_align(values[i], LV_TEXT_ALIGN_RIGHT, 0);
    }
    int y = ROW_Y - ROWS_Y + 6 * ROW_DY;
    rule(rows, 0, y - 12);
    heading = text(rows, font_text_12, C_DAY, 2, y + 12, 0);
    lv_obj_set_style_text_letter_space(heading, 2, 0);
    for (int k = 0; k < 4; k++) {
        over[k] = text(rows, f15, COL_TEXT, 2, y + 36 + 24 * k, OVER_W);
    }
    nolocation = text(view, f15, COL_DIM, COL_X + 2, ROW_Y, 0);
    lv_label_set_text(nolocation, "Set WEATHER_LAT/LON for the sky");
    lv_obj_set_hidden(nolocation, true);
    column();
}
