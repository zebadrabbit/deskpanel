// The tornado takeover, whatever the layout (takeover.c): the sticky notice on the top layer, its OK, the
// jump to the radar card's tab (skipped with no radar card) and the weather alert sheet. All on the LVGL task.
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "weather.h"

void takeover_init(void);                 // own 100 ms timer: takes the weather document with its own seen counter
bool takeover_active(void);               // a take alert is active (the radar card's red frame + loop-only)
bool takeover_notice_open(void);          // its notice is up (not yet OK'd)
void takeover_open_sheet(int alert_index); // the alert sheet, also used by the radar card's badge
// Puts the notice back in front of a toast or pop-up that just came up, or after a sheet closes. Never in front
// of an open sheet: one opened later covers it until it closes. Only ever brings it forward.
void takeover_raise(void);
int32_t takeover_notice_top(void); // the notice's top edge (screen y) while it's up, else -1

// The alert look, shared with the radar card's badge and frame chip.
#define WX_RED      0xe5484d // warnings, tornado outlines, the takeover
#define WX_AMBER    0xe0a020 // watches, and the chip of a stale radar frame
#define WX_ADVISORY 0xc9a227 // advisories and statements
uint32_t wx_color(const char *cls);                             // an alert class's colour
uint32_t wx_ink(const char *cls);                               // the text colour on it
const char *wx_hhmm(char *buf, size_t n, long long t);          // "2:52 PM", local time
const char *wx_until(char *buf, size_t n, const wx_alert_t *a); // "until 3:30 PM", "" with no end time
