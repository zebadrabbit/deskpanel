// Network task: Wi-Fi with reconnect backoff, SNTP, the RTC as clock fallback, and the
// /api/v1/panel, /weather, /cards and /astro pollers. Nothing here touches LVGL; the UI reads these from its own timer.
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "astro.h"
#include "cards.h"
#include "layout.h"
#include "panel.h"
#include "weather.h"

typedef enum { NET_OFF, NET_CONNECTING, NET_UP } net_state_t;

typedef struct {
    net_state_t state;
    int rssi;     // dBm while NET_UP
    bool time_ok; // system clock set, from NTP or the RTC
} net_status_t;

void net_start(void);
net_status_t net_status(void); // cheap snapshot; safe from any task, never blocks
bool net_wifi_unset(void);      // WIFI_SSID is empty (built in and settings.txt): Wi-Fi stays off. Fixed after net_start

// Copy the newest panel document into *out if it's newer than *seen (and update *seen).
// Never blocks: if the network task is mid-copy it returns false and the UI tries next tick.
bool net_panel_take(panel_t *out, uint32_t *seen);

// Milliseconds since the last good poll (200 or 304), or -1 before the first one.
int64_t net_panel_age_ms(void);

// Mark a Planka card done: queues POST /api/v1/planka/cards/{id}/done for the network task.
// Returns false if the queue is full. Never blocks.
bool net_mark_done(const char *card_id);

typedef struct {
    char card_id[24];
    int status; // HTTP status from the aggregator (204 = done), -1 = couldn't reach it
} net_done_t;

// A finished mark-done request, if one is waiting. Never blocks.
bool net_done_result(net_done_t *out);

// Queue POST /api/v1/lights/<card>/<action> (action: "toggle", "on", "off", "bri/15", "color/blue") or
// /api/v1/profiles/<card>/<action> ("apply", "save"). False if the queue is full. Never blocks.
bool net_light(const char *card_id, const char *action);
bool net_profile(const char *card_id, const char *action);
typedef struct { char card_id[24]; char action[16]; int status; } net_act_t; // 204 done, -1 unreachable
bool net_act_result(net_act_t *out); // a finished light or profile request, if one is waiting. Never blocks.

// Tell the aggregator every note up to `upto` has been read (OK tapped / history opened).
// Fire and forget: if it fails, the next read report covers it. Never blocks.
bool net_mark_read(int upto);

// Copy the newest weather document into *out if it's newer than *seen. Same contract as net_panel_take.
bool net_weather_take(weather_t *out, uint32_t *seen);

// The last GET /api/v1/weather status: 200/304 ok, 404 weather isn't set up on the aggregator,
// -1 unreachable, 0 not polled yet. Never blocks.
int net_weather_status(void);

// Copy the newest layout document into *out if it's newer than *seen. Same contract as net_panel_take;
// the first take after boot returns the copy saved on the SD card, if there is one. Never blocks.
bool net_layout_take(layout_t *out, uint32_t *seen);

// The last GET /api/v1/layout status: 200/304 ok, 404 aggregator too old, -1 unreachable, 5xx aggregator not
// ready, keeping what's shown (nothing is published or saved), 0 not polled yet.
int net_layout_status(void);

// Copy the newest /api/v1/cards document into *out if it's newer than *seen. Same contract as net_panel_take;
// the first take after boot returns the copy saved on the SD card, if there is one. Never blocks.
bool net_cards_take(cards_t *out, uint32_t *seen);

// Milliseconds since the last good /api/v1/cards poll (200 or 304), or -1 before the first one.
int64_t net_cards_age_ms(void);

// The last GET /api/v1/cards status, as net_layout_status: 404 means the aggregator is too old (polled every 60 s).
int net_cards_status(void);

// The newest weather document's radar frames that are fully inflated, oldest first: px[i] is the
// 800x368 RGB565 image valid at t[i]. n is below WX_FRAMES while frames are still arriving.
typedef struct {
    int n;
    long long t[WX_FRAMES];
    const uint16_t *px[WX_FRAMES];
} net_frames_t;

// Like net_panel_take, for the frame set (one caller: the Weather page). The px buffers hold still until
// the next take that returns true; after that the net task may overwrite the old set's, so switch to the
// new pointers in the same tick. Keep calling it: a buffer is only reused once the UI has moved past it.
bool net_frames_take(net_frames_t *out, uint32_t *seen);

// Queue GET /api/v1/weather/alert/{n} for the network task. Returns false if the queue is full. Never blocks.
bool net_fetch_alert(int n);

// A finished alert fetch, if one is waiting: which alert, its HTTP status (200 ok, -1 unreachable) and the
// body (JSON for weather_alert_text), cut to fit buf. A newer fetch replaces an untaken one. Never blocks.
bool net_alert_take(int *n, int *status, char *buf, size_t cap);

// Copy the newest /api/v1/astro document into *out if it's newer than *seen. Same contract as net_panel_take;
// the first take after boot returns the copy saved on the SD card, if there is one. Never blocks.
bool net_astro_take(astro_t *out, uint32_t *seen);

// Log the internal-RAM and PSRAM free sizes as "<why>: internal ...". A no-op in the sim.
void net_log_heap(const char *why);

// The last GET /api/v1/astro status: 200/304 ok, 404 no location set on the aggregator (polled every 10 min),
// 5xx aggregator not ready (keeping what's shown), -1 unreachable, 0 not polled yet. Never blocks.
int net_astro_status(void);

// The current complete sky picture (the front buffer): 378x378 little-endian RGB565, valid at *t. Returns true
// when it is newer than *seen (and updates *seen). *px stays valid until the next take that returns true, after
// which the net task may reuse that buffer for a download; switch to the new pointer in the same tick. The first
// take after boot returns the picture saved on the SD card, if there is one. Never blocks.
bool net_sky_take(const uint16_t **px, long long *t, uint32_t *seen);

// Milliseconds since the last good /api/v1/astro poll (200 or 304), or -1 before the first one.
int64_t net_astro_age_ms(void);
