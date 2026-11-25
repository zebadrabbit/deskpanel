// The aggregator's /api/v1/panel document as plain structs. The network task parses into one and
// hands the UI a copy. Sizes follow the aggregator's caps (aggregator/*.go) with a little headroom.
#pragma once

#include <stdbool.h>
#include <stddef.h>

#define PANEL_MAX_GPUS   4
#define PANEL_MAX_POOLS  7 // = maxDefaultPools (aggregator/layout_default.go): one card per pool kept
#define PANEL_MAX_ALERTS 8
#define PANEL_MAX_PEERS  16
#define PANEL_MAX_SVCS   6
#define PANEL_MAX_BOARDS 6
#define PANEL_MAX_CARDS  3
#define PANEL_MAX_LABELS 2
#define PANEL_MAX_NOTES  5

typedef struct {
    char host[16], name[24], state[8]; // state: idle | loaded | busy | off
    bool up;                           // agent reachable
    int temp, util, vram, vram_total, power, procs; // °C, %, MiB, MiB, W, compute processes
} panel_gpu_t;

typedef struct {
    char name[28], health[12]; // health: ONLINE, DEGRADED, FAULTED...
    bool ok;
    int used_gib, avail_gib;
} panel_pool_t;

typedef struct {
    char level[12], msg[164]; // level: INFO, NOTICE, WARNING, ERROR, CRITICAL, ALERT, EMERGENCY
} panel_alert_t;

typedef struct {
    char name[24];
    bool on;
} panel_peer_t;

typedef struct {
    char name[20];
    unsigned color; // 0xRRGGBB
} panel_label_t;

typedef struct {
    char id[24], name[60], list[28]; // UTF-8
    long long due;                   // unix seconds, 0 = none
    bool done;                       // already in the board's done list
    int nlabels;
    panel_label_t labels[PANEL_MAX_LABELS];
} panel_card_t;

typedef struct {
    char id[24], name[28];
    bool can_done; // the board has a done list
    int ncards;
    panel_card_t cards[PANEL_MAX_CARDS]; // most recently updated first
} panel_board_t;

typedef struct {
    int id;      // rising; the panel reports reads "up to id"
    long long t; // unix seconds
    char src[16], msg[92], lvl[8]; // lvl: info | warn | alert
    char act[4];                   // action on tap: "" | "wx" (open the Weather tab)
    bool read;
} panel_note_t;

typedef struct panel {
    bool homelab_stale, planka_stale; // a collector failed its last run on the aggregator
    int ngpus, npools, nalerts, npeers, nsvcs, info_alerts, nboards, nnotes;
    panel_note_t notes[PANEL_MAX_NOTES]; // newest first
    panel_gpu_t gpus[PANEL_MAX_GPUS];
    panel_pool_t pools[PANEL_MAX_POOLS];    // the boot pool comes last
    panel_alert_t alerts[PANEL_MAX_ALERTS]; // most severe first, INFO/NOTICE last
    panel_peer_t peers[PANEL_MAX_PEERS];
    panel_peer_t svcs[PANEL_MAX_SVCS]; // HTTP health checks (e.g. Jellyfin): name + up
    panel_board_t boards[PANEL_MAX_BOARDS];
} panel_t;

// Parse a /api/v1/panel body into *out. Missing or mistyped fields read as empty/zero, arrays stop at
// their caps, strings are cut to fit on a UTF-8 boundary. Returns false only if the body isn't a
// JSON object.
bool panel_parse(const char *json, size_t len, panel_t *out);

// A layout card's source in the document, or NULL. GPU src: "host" is that host's first GPU, "host#2" its
// second, and so on. The src is cut the way panel_parse cuts the field (UTF-8 safe), so a name longer
// than the field still matches.
const panel_gpu_t *panel_gpu(const panel_t *p, const char *src);
const panel_pool_t *panel_pool(const panel_t *p, const char *src);
