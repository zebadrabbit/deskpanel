// settings.h: the panel's settings file, settings.txt in the SD card's root, KEY=value lines over the values built
// in from secrets.h. Plain C, no ESP-IDF, so test/settings_test.c can run it on a PC.
#pragma once

#include <stddef.h>

#define SETTINGS_FILE_MAX 4096 // a bigger file is ignored whole, never half-read
#define SETTINGS_KEYS 6

typedef struct {
    char wifi_ssid[32], wifi_password[64]; // wifi_config_t's sizes: what fits here fits there
    char aggregator_url[96];               // + the longest API path (49 B) fits net.c's 160-byte URLs
    char panel_token[96];
    char tz[64], ntp_server[64];
} settings_t;

extern const char *const settings_key[SETTINGS_KEYS]; // "WIFI_SSID", "WIFI_PASSWORD", "AGGREGATOR_URL", "PANEL_TOKEN",
                                                      // "DESKPANEL_TZ", "NTP_SERVER": settings_t's fields in order

// Applies text (len bytes; need not end in '\n' or NUL) to s; returns the keys set, bit i = settings_key[i].
// A line is blank, a comment (first non-blank is '#') or KEY=value. A leading UTF-8 BOM, a CR at a line's end, and
// spaces/tabs around KEY and value are dropped; the value is otherwise taken as is (no quotes; '#' and '=' are part of
// it); keys are exact (case matters); the last line for a key wins; an empty value sets "". AGGREGATOR_URL loses
// trailing '/', then must be "http://" + a host (letters, digits, '.', '-', '_') + optionally ":" and a port 1..65535
// (1-5 digits) + optionally a path ("/…", printable non-space ASCII). Unknown keys, lines without '=', values that
// don't fit their field and a bad AGGREGATOR_URL leave s alone and call warn() with "settings.txt line N: …", naming
// at most the key (an unknown one only if it is all A-Z, 0-9 and '_', cut to 32 bytes), never a value.
unsigned settings_parse(settings_t *s, const char *text, size_t len, void (*warn)(const char *msg));
