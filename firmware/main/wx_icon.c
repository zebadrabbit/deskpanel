// Weather icons from LVGL shapes, laid out on the mockup's 170x150 grid and scaled to the icon's size:
// a cloud is three discs on a flat base, the sun a disc with round-capped rays, the moon a crescent (a
// disc's offset shadow minus the disc), the bolt two overlapping triangles drawn in a draw event. Round on the glass:
// every x is PANEL_ASPECT narrower about the icon's middle column (sx()), so a disc is a stadium as the clock's are.
#include "wx_icon.h"

#include <math.h>

#include "astro.h"

#define GRID    170
#define C_SUN   0xffc83d
#define C_CLOUD 0xe8edf3
#define C_RAIN  0x4aa3ff
#define C_MOON  0xf5e3a8
#define C_SNOW  0xcfe6ff
#define C_FOG   0xa9b3bf

enum { K_SUN, K_MOON, K_SUN_CLOUD, K_MOON_CLOUD, K_CLOUD, K_FOG, K_RAIN, K_SNOW, K_STORM };

typedef struct {
    int size, kind, nlines;
    lv_point_precise_t pts[8][2]; // lv_line keeps a pointer to its points
} icon_t;

static int s(const icon_t *ic, int v) { return v * ic->size / GRID; }

// An x, also narrowed about the middle column; the box stays size wide, so the overlay's layout doesn't move.
static int sx(const icon_t *ic, int v)
{
    return (int)lroundf((v - GRID / 2) * ic->size / (GRID * PANEL_ASPECT)) + ic->size / 2;
}

static lv_obj_t *shape(lv_obj_t *box, int x, int y, int w, int h, int radius, uint32_t color)
{
    icon_t *ic = lv_obj_get_user_data(box);
    lv_obj_t *o = lv_obj_create(box);
    lv_obj_remove_style_all(o);
    lv_obj_set_pos(o, sx(ic, x), s(ic, y));
    lv_obj_set_size(o, sx(ic, x + w) - sx(ic, x), s(ic, y + h) - s(ic, y));
    lv_obj_set_style_radius(o, radius, 0);
    lv_obj_set_style_bg_color(o, lv_color_hex(color), 0);
    lv_obj_set_style_bg_opa(o, LV_OPA_COVER, 0);
    return o;
}

static void disc(lv_obj_t *box, int cx, int cy, int r, uint32_t color)
{
    shape(box, cx - r, cy - r, 2 * r, 2 * r, LV_RADIUS_CIRCLE, color);
}

static void line(lv_obj_t *box, int x1, int y1, int x2, int y2, int w, uint32_t color)
{
    icon_t *ic = lv_obj_get_user_data(box);
    lv_point_precise_t *p = ic->pts[ic->nlines++];
    p[0] = (lv_point_precise_t){sx(ic, x1), s(ic, y1)};
    p[1] = (lv_point_precise_t){sx(ic, x2), s(ic, y2)};
    lv_obj_t *l = lv_line_create(box);
    lv_line_set_points(l, p, 2);
    lv_obj_set_style_line_width(l, LV_MAX(1, s(ic, w)), 0);
    lv_obj_set_style_line_color(l, lv_color_hex(color), 0);
    lv_obj_set_style_line_rounded(l, true, 0);
}

// Rays from 1.3 r to 1.9 r every 45°; `rays` is a bit mask, bit 0 = up, then clockwise.
static void sun(lv_obj_t *box, int cx, int cy, int r, unsigned rays)
{
    static const int8_t dir[8][2] = {{0, -10}, {7, -7}, {10, 0}, {7, 7}, {0, 10}, {-7, 7}, {-10, 0}, {-7, -7}};
    for (int i = 0; i < 8; i++) {
        if (rays & 1u << i) {
            line(box, cx + dir[i][0] * r * 13 / 100, cy + dir[i][1] * r * 13 / 100, cx + dir[i][0] * r * 19 / 100,
                 cy + dir[i][1] * r * 19 / 100, r * 32 / 100, C_SUN);
        }
    }
    disc(box, cx, cy, r, C_SUN);
}

// A crescent centred on (cx, cy): LVGL never draws an object's shadow underneath the object, so a
// transparent disc whose shadow is offset down-left leaves just the shadow's lit sliver.
static void moon(lv_obj_t *box, int cx, int cy, int r)
{
    icon_t *ic = lv_obj_get_user_data(box);
    int d = r * 55 / 100;
    lv_obj_t *o = shape(box, cx - r + d, cy - r - d / 3, 2 * r, 2 * r, LV_RADIUS_CIRCLE, C_MOON);
    lv_obj_set_style_bg_opa(o, LV_OPA_TRANSP, 0);
    lv_obj_set_style_shadow_color(o, lv_color_hex(C_MOON), 0);
    lv_obj_set_style_shadow_opa(o, LV_OPA_COVER, 0);
    lv_obj_set_style_shadow_width(o, 2, 0);
    lv_obj_set_style_shadow_offset_x(o, sx(ic, cx - r) - sx(ic, cx - r + d), 0); // the lit disc centred on cx
    lv_obj_set_style_shadow_offset_y(o, s(ic, d / 3), 0);
}

static void cloud(lv_obj_t *box, int dx, int dy) // the mockup's cloud, x 29..162, y 48..128, moved by dx, dy
{
    shape(box, 48 + dx, 100 + dy, 88, 28, 0, C_CLOUD);
    disc(box, 58 + dx, 99 + dy, 29, C_CLOUD);
    disc(box, 101 + dx, 84 + dy, 36, C_CLOUD);
    disc(box, 136 + dx, 102 + dy, 26, C_CLOUD);
}

static void draw_bolt(lv_event_t *e)
{
    static const int16_t tri[2][3][2] = {{{112, 70}, {72, 116}, {104, 110}}, {{86, 100}, {118, 96}, {78, 146}}};
    lv_obj_t *o = lv_event_get_target_obj(e);
    icon_t *ic = lv_obj_get_user_data(lv_obj_get_parent(o));
    lv_area_t a;
    lv_obj_get_coords(o, &a);
    lv_draw_triangle_dsc_t d;
    lv_draw_triangle_dsc_init(&d);
    d.color = lv_color_hex(C_SUN);
    d.opa = lv_obj_get_style_opa_recursive(o, LV_PART_MAIN); // shapes fade with their page; this one by hand
    for (int t = 0; t < 2; t++) {
        for (int k = 0; k < 3; k++) {
            d.p[k].x = a.x1 + sx(ic, tri[t][k][0]);
            d.p[k].y = a.y1 + s(ic, tri[t][k][1]);
        }
        lv_draw_triangle(lv_event_get_layer(e), &d);
    }
}

static void bolt(lv_obj_t *box)
{
    lv_obj_t *o = lv_obj_create(box);
    lv_obj_remove_style_all(o);
    lv_obj_set_size(o, LV_PCT(100), LV_PCT(100));
    lv_obj_add_event_cb(o, draw_bolt, LV_EVENT_DRAW_MAIN, NULL);
}

static int kind_of(int wmo, bool day)
{
    if (wmo == 0 || wmo == 1) {
        return day ? K_SUN : K_MOON;
    }
    if (wmo == 2) {
        return day ? K_SUN_CLOUD : K_MOON_CLOUD;
    }
    if (wmo == 45 || wmo == 48) {
        return K_FOG;
    }
    if ((wmo >= 51 && wmo <= 67) || (wmo >= 80 && wmo <= 82)) {
        return K_RAIN;
    }
    if ((wmo >= 71 && wmo <= 77) || wmo == 85 || wmo == 86) {
        return K_SNOW;
    }
    if (wmo >= 95 && wmo <= 99) {
        return K_STORM;
    }
    return K_CLOUD; // 3, and any code we don't know
}

static void on_delete(lv_event_t *e) { lv_free(lv_event_get_user_data(e)); }

lv_obj_t *wx_icon_create(lv_obj_t *parent, int size)
{
    icon_t *ic = lv_malloc_zeroed(sizeof *ic);
    LV_ASSERT_MALLOC(ic);
    ic->size = size;
    ic->kind = -1;
    lv_obj_t *box = lv_obj_create(parent);
    lv_obj_remove_style_all(box);
    lv_obj_set_size(box, size, size * 15 / 17);
    lv_obj_set_user_data(box, ic);
    lv_obj_add_event_cb(box, on_delete, LV_EVENT_DELETE, ic);
    return box;
}

void wx_icon_set(lv_obj_t *box, int wmo, bool day)
{
    icon_t *ic = lv_obj_get_user_data(box);
    int k = kind_of(wmo, day);
    if (k == ic->kind) {
        return;
    }
    ic->kind = k;
    ic->nlines = 0;
    lv_obj_clean(box);
    switch (k) {
    case K_SUN:
        sun(box, 85, 75, 30, 0xff);
        break;
    case K_MOON:
        moon(box, 85, 75, 40);
        break;
    case K_SUN_CLOUD: // as in the mockup: the rays the cloud would hide aren't drawn
        sun(box, 62, 50, 22, 0xc3);
        cloud(box, 0, 0);
        break;
    case K_MOON_CLOUD:
        moon(box, 66, 50, 28);
        cloud(box, 0, 0);
        break;
    case K_CLOUD:
        cloud(box, -10, -13);
        break;
    case K_FOG:
        cloud(box, -10, -36);
        line(box, 34, 106, 136, 106, 8, C_FOG);
        line(box, 50, 124, 150, 124, 8, C_FOG);
        line(box, 30, 142, 120, 142, 8, C_FOG);
        break;
    case K_RAIN:
        cloud(box, -10, -36);
        for (int x = 60; x <= 124; x += 32) {
            line(box, x, 104, x - 10, 134, 7, C_RAIN);
        }
        break;
    case K_SNOW:
        cloud(box, -10, -36);
        for (int i = 0; i < 5; i++) { // a row of three, then two in the gaps
            disc(box, i < 3 ? 52 + i * 34 : 69 + (i - 3) * 34, i < 3 ? 110 : 136, 7, C_SNOW);
        }
        break;
    default: // K_STORM
        cloud(box, -10, -36);
        bolt(box);
        line(box, 52, 104, 42, 134, 7, C_RAIN);
        line(box, 136, 104, 126, 134, 7, C_RAIN);
        break;
    }
}
