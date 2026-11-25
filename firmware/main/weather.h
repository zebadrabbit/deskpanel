// The aggregator's /api/v1/weather document as plain structs, plus the tornado-takeover bookkeeping.
// Plain C + cJSON, no ESP-IDF, so test/weather_test.c can run it on a PC.
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define WX_FRAMES     5
#define WX_MAX_ALERTS 4
#define WX_MAX_POLYS  8
#define WX_MAX_PTS    16

typedef struct {
    int t, rh, code, hi, lo;
    bool day;
    char text[24], wind[16];
} wx_now_t;

typedef struct {
    int n, lvl;
    long long until;
    bool take;
    char key[12], event[40], cls[10];
} wx_alert_t;

typedef struct {
    char kind; // 't' | 's'
    bool home;
    int npts;
    int16_t pts[WX_MAX_PTS][2];
} wx_poly_t;

typedef struct {
    bool stale, has_now;
    long long updated_at;
    wx_now_t now;
    int nframes;
    long long frames[WX_FRAMES]; // oldest first
    int nalerts;
    wx_alert_t alerts[WX_MAX_ALERTS]; // most severe first
    int npolys;
    wx_poly_t polys[WX_MAX_POLYS];
} weather_t;

// Parse a /api/v1/weather body. Null or missing pieces read as empty; arrays stop at their caps.
// Returns false only if the body isn't a JSON object.
bool weather_parse(const char *json, size_t len, weather_t *w);

// A full alert (GET /api/v1/weather/alert/{n}) as the four parts the alert sheet sets, each pointing into out
// ("" when absent or cut off): head, when ("Issued 2:52 PM · expires 3:30 PM" in local time; a missing time,
// <= 0, is left out: "Issued X", "Expires Y" or ""), desc and instr. NUL-separated in out, cut as a whole to
// fit cap (every part's NUL included) on a UTF-8 boundary. Returns false if the body isn't a JSON object.
typedef struct {
    const char *head, *when, *desc, *instr;
} wx_text_t;
bool weather_alert_text(const char *json, size_t len, char *out, size_t cap, wx_text_t *t);

// One paragraph (len bytes, no NUL needed) as LVGL recolor text: every '#' doubled, and a leading keyword (an
// optional "* ", then 2+ of A-Z, space, '/' or '-', then "...") wrapped as "#ffd35a KEYWORD...#". Returns the
// length written; cap >= 2 * len + 12, so nothing is cut.
size_t wx_markup(const char *para, size_t len, char *out, size_t cap);

// True if the alert body is the one chip `a` names: same event (cut to a->event's size, as weather_parse
// cuts it) and same expires as a->until. False for a non-object body.
bool weather_alert_matches(const char *json, size_t len, const wx_alert_t *a);

// Alerts the user has dismissed: key + the level they were dismissed at.
typedef struct {
    int n;
    struct {
        char key[12];
        int lvl;
    } a[8];
} wx_acks_t;

// The badge and takeover-notice wording for alert a, whose end reads `until` ("until 3:30 PM", or ""). A
// Tornado Warning's wording follows its lvl, so an escalation never looks like the notice already OK'd:
//   badge  "TORNADO OBSERVED · until 3:30 PM" (other alerts: the event in capitals; the icon is the caller's)
//   notice "Tornado Warning for your area until 3:30 PM — tornado observed. Take shelter now."
void wx_badge_text(const wx_alert_t *a, const char *until, char *out, size_t cap);
void wx_notice_text(const wx_alert_t *a, const char *until, char *out, size_t cap);

int wx_takeover_due(const weather_t *w, const wx_acks_t *acks); // first take alert not acked at >= its lvl, else -1
void wx_ack(wx_acks_t *acks, const char *key, int lvl);         // add or raise; full -> evict the oldest

// Whether to publish a set of n frames: an empty one only when nothing is shown or every buffer is busy (the
// caller then sheds the oldest shown frame rather than publishing nothing).
bool wx_frames_publish(int n, unsigned pub_mask, unsigned busy_mask, int nbuf);
