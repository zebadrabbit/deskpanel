// Round on the glass: the panel's pixels are PANEL_ASPECT (main/astro.h) wider than tall, so whatever is round is
// drawn that much narrower. The astronomical clock's rings and the cards' gauges are ellipses drawn as fitted arcs
// of circles (ellipse.c). All on the LVGL task.
#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "astro.h"
#include "lvgl.h"

#define ELLIPSE_ARCS 8 // arcs to a ring

// A ring is an ellipse, but LVGL draws round arcs only: each ring is ELLIPSE_ARCS arcs of circles, each fitted to its
// 45° (ellipse_fit()). Lines would do, but LVGL culls a line by its ends ± width / 2 while its anti-aliasing reaches
// further, so a redraw's edge (the clock's hands each minute, the sky every 2 min) drops pixels from it; an arc is
// culled exactly.
typedef struct {
    int16_t cx, cy, r;  // the circle: centre from the ring's, outer radius
    int16_t start, end; // LVGL's degrees from that centre (0 = 3 o'clock, clockwise): its 45° and 1° into the next
} ellipse_arc_t;

int ellipse_w(int h); // the width that's h on the glass: lroundf(h / PANEL_ASPECT)
// Fits the ring to the ellipse at physical radius r (as tall as it is, 1/PANEL_ASPECT as wide), a w px band on it.
void ellipse_fit(ellipse_arc_t ring[ELLIPSE_ARCS], float r, int w);
// The ring's arcs from deg (clockwise from up) for span degrees, cut there to the degree, into out (ELLIPSE_ARCS + 1
// at most). Returns how many.
int ellipse_cut(ellipse_arc_t *out, const ellipse_arc_t ring[ELLIPSE_ARCS], float r, float deg, float span);
// Arcs, w px wide, in an opaque colour, centred on centre: each runs into the next, which covers its end (no seam,
// and no blending twice). caps rounds the first arc's start and the last's end (LVGL rounds both ends of each; a cap
// at a joint lies inside the band, in the same colour).
void ellipse_draw(lv_layer_t *layer, const ellipse_arc_t *arcs, int n, lv_point_t centre, int w, lv_color_t c,
                  bool caps);
// An lv_arc (sized, styled, no padding; track and value the same width) drawn round on the glass from now on: its
// own angles, colours, widths and caps, as ellipses.
void ellipse_gauge(lv_obj_t *arc);
void ellipse_gauge_set(lv_obj_t *arc, int32_t v); // lv_arc_set_value(), then the whole arc redraws; no-op if unchanged
