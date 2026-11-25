// Round on the glass (ellipse.h): fitted ellipse arcs, for the astronomical clock's rings and the cards' gauges.
#include "ellipse.h"

#include <math.h>

int ellipse_w(int h) { return (int)lroundf(h / PANEL_ASPECT); }

static int16_t norm(int deg) { return (int16_t)((deg % 360 + 360) % 360); }

// The point at physical radius r from the ring's centre, deg clockwise from up (unrounded), seen from arc a's
// centre: LVGL's degrees.
static float seen_from(const ellipse_arc_t *a, float r, float deg)
{
    float t = deg * (float)M_PI / 180;
    return atan2f(-r * cosf(t) - a->cy, r * sinf(t) / PANEL_ASPECT - a->cx) * 180 / (float)M_PI;
}

// Fits each arc to its 45° of the ellipse at physical radius r, a w px band centred on it: the whole-pixel centre
// (around the circle through the ends and the middle) and outer radius that miss 9 points along it by the least, at
// most half a pixel (whole pixels can't make the narrowed width exactly).
void ellipse_fit(ellipse_arc_t ring[ELLIPSE_ARCS], float r, int w)
{
    for (int k = 0; k < ELLIPSE_ARCS; k++) {
        float x[9], y[9], best = r;
        for (int j = 0; j < 9; j++) {
            float t = (k + j / 8.0f) * (360.0f / ELLIPSE_ARCS) * (float)M_PI / 180;
            x[j] = r * sinf(t) / PANEL_ASPECT;
            y[j] = -r * cosf(t);
        }
        float s0 = x[0] * x[0] + y[0] * y[0], s4 = x[4] * x[4] + y[4] * y[4], s8 = x[8] * x[8] + y[8] * y[8];
        float d = 2 * (x[0] * (y[4] - y[8]) + x[4] * (y[8] - y[0]) + x[8] * (y[0] - y[4]));
        int ux = (int)lroundf((s0 * (y[4] - y[8]) + s4 * (y[8] - y[0]) + s8 * (y[0] - y[4])) / d);
        int uy = (int)lroundf((s0 * (x[8] - x[4]) + s4 * (x[0] - x[8]) + s8 * (x[4] - x[0])) / d);
        for (int cx = ux - 1; cx <= ux + 1; cx++) {
            for (int cy = uy - 1; cy <= uy + 1; cy++) {
                float lo = 2 * r, hi = 0;
                for (int j = 0; j < 9; j++) {
                    float dj = hypotf(x[j] - cx, y[j] - cy);
                    lo = fminf(lo, dj);
                    hi = fmaxf(hi, dj);
                }
                for (int ro = (int)floorf((lo + hi + w) / 2); ro <= (int)ceilf((lo + hi + w) / 2); ro++) {
                    float miss = fmaxf(hi - (ro - w / 2.0f), (ro - w / 2.0f) - lo);
                    if (miss < best) {
                        best = miss;
                        ring[k] = (ellipse_arc_t){(int16_t)cx, (int16_t)cy, (int16_t)ro, 0, 0};
                    }
                }
            }
        }
        ring[k].start = norm((int)floorf(seen_from(&ring[k], r, k * 360.0f / ELLIPSE_ARCS)));
        ring[k].end = norm((int)ceilf(seen_from(&ring[k], r, (k + 1) * 360.0f / ELLIPSE_ARCS)) + 1);
    }
}

int ellipse_cut(ellipse_arc_t *out, const ellipse_arc_t ring[ELLIPSE_ARCS], float r, float a, float span)
{
    int n = 0;
    float end = a + span, sector = 360.0f / ELLIPSE_ARCS;
    while (a < end && n <= ELLIPSE_ARCS) {
        float b = fminf(end, (floorf(a / sector) + 1) * sector);
        ellipse_arc_t *p = &out[n];
        *p = ring[(int)floorf(a / sector) % ELLIPSE_ARCS];
        if (!n++) {
            p->start = norm((int)lroundf(seen_from(p, r, a)));
        }
        if (b >= end) {
            p->end = norm((int)lroundf(seen_from(p, r, b)));
        }
        a = b;
    }
    return n;
}

void ellipse_draw(lv_layer_t *layer, const ellipse_arc_t *arcs, int n, lv_point_t centre, int w, lv_color_t c,
                  bool caps)
{
    lv_draw_arc_dsc_t d;
    lv_draw_arc_dsc_init(&d);
    d.width = w;
    d.color = c;
    for (int k = 0; k < n; k++) {
        d.center = (lv_point_t){centre.x + arcs[k].cx, centre.y + arcs[k].cy};
        d.radius = arcs[k].r;
        d.start_angle = arcs[k].start;
        d.end_angle = arcs[k].end;
        d.rounded = caps && (k == 0 || k == n - 1);
        lv_draw_arc(layer, &d);
    }
}

typedef struct {
    ellipse_arc_t ring[ELLIPSE_ARCS];
    float r; // the band's middle, physical px
    int R;   // the lv_arc's radius: half its width
} gauge_t;

// One part of the lv_arc, a0 to a1 in its degrees, as lv_arc would draw it but round on the glass.
static void part(lv_layer_t *layer, lv_obj_t *arc, const gauge_t *g, lv_part_t pt, int a0, int a1, lv_opa_t opa)
{
    int span = ((a1 - a0) % 360 + 360) % 360; // 135 -> 45 is 270; an empty value is 0
    if (!span) {
        return;
    }
    ellipse_arc_t out[ELLIPSE_ARCS + 1];
    int from = ((a0 + lv_arc_get_rotation(arc) + 90) % 360 + 360) % 360; // LVGL's 0 is 3 o'clock
    int n = ellipse_cut(out, g->ring, g->r, (float)from, (float)span);
    lv_area_t a;
    lv_obj_get_coords(arc, &a);
    lv_area_t c = a; // the layer's area
    lv_point_t centre = {a.x1 + g->R, a.y1 + g->R};
    lv_layer_t *l = layer;
    if (opa < LV_OPA_MAX) {
        // See-through (a stale card, an offline GPU): through a layer, so the arcs' overlaps blend once, as lv_arc's
        // one arc did. Only as big as this redraw needs: a layer takes its area as given, unclipped.
        const lv_area_t *clip = &layer->_clip_area;
        c = (lv_area_t){LV_MAX(a.x1, clip->x1), LV_MAX(a.y1, clip->y1), LV_MIN(a.x2, clip->x2), LV_MIN(a.y2, clip->y2)};
        if (c.x1 > c.x2 || c.y1 > c.y2) {
            return;
        }
        l = lv_draw_layer_create(layer, LV_COLOR_FORMAT_ARGB8888, &c);
    }
    ellipse_draw(l, out, n, centre, lv_obj_get_style_arc_width(arc, pt), lv_obj_get_style_arc_color(arc, pt),
                 lv_obj_get_style_arc_rounded(arc, pt));
    if (l != layer) {
        lv_draw_image_dsc_t d;
        lv_draw_image_dsc_init(&d);
        d.src = l;
        d.opa = opa;
        lv_draw_layer(layer, &d, &c);
    }
}

static void draw_gauge(lv_event_t *e) // the track, then the value over it: each part blends once, as lv_arc's two
{
    lv_obj_t *arc = lv_event_get_target_obj(e);
    const gauge_t *g = lv_event_get_user_data(e);
    lv_opa_t opa = lv_obj_get_style_opa_recursive(arc, LV_PART_MAIN);
    if (opa <= LV_OPA_MIN) {
        return;
    }
    lv_layer_t *layer = lv_event_get_layer(e);
    part(layer, arc, g, LV_PART_MAIN, lv_arc_get_bg_angle_start(arc), lv_arc_get_bg_angle_end(arc), opa);
    part(layer, arc, g, LV_PART_INDICATOR, lv_arc_get_angle_start(arc), lv_arc_get_angle_end(arc), opa);
}

static void free_gauge(lv_event_t *e) { lv_free(lv_event_get_user_data(e)); }

void ellipse_gauge(lv_obj_t *arc)
{
    gauge_t *g = lv_malloc(sizeof *g);
    LV_ASSERT_MALLOC(g);
    int w = lv_obj_get_style_arc_width(arc, LV_PART_MAIN);
    g->R = lv_obj_get_style_width(arc, LV_PART_MAIN) / 2; // as set: the layout hasn't run yet
    // The band's middle, so its outer edge is round on the glass: R tall, R / PANEL_ASPECT wide (the band is w px
    // across everywhere; R - w / 2 would leave the 100 px gauge 96 px wide, not 94).
    g->r = g->R - w * PANEL_ASPECT / 2;
    ellipse_fit(g->ring, g->r, w); // once, here
    lv_obj_set_style_arc_opa(arc, LV_OPA_TRANSP, LV_PART_MAIN); // lv_arc itself draws nothing
    lv_obj_set_style_arc_opa(arc, LV_OPA_TRANSP, LV_PART_INDICATOR);
    lv_obj_add_event_cb(arc, draw_gauge, LV_EVENT_DRAW_MAIN, g);
    lv_obj_add_event_cb(arc, free_gauge, LV_EVENT_DELETE, g);
}

void ellipse_gauge_set(lv_obj_t *arc, int32_t v)
{
    int32_t was = lv_arc_get_value(arc);
    lv_arc_set_value(arc, v); // clamped to the range, and invalidates the round arc's change only
    if (lv_arc_get_value(arc) != was) {
        lv_obj_invalidate(arc); // the ellipse pokes out of that box
    }
}
