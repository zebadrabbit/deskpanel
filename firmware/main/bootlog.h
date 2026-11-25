// The boot log's two pure helpers. Plain C, no ESP-IDF, so test/bootlog_test.c can run them on a PC.
#pragma once

#include <stddef.h>
#include <stdint.h>

#define BOOTLOG_MAX 16384

// Appends line (< BOOTLOG_MAX, ending '\n') to the log text in buf (len bytes; a last line a power cut left without
// its '\n' gets one first), dropping the oldest whole lines until it fits BOOTLOG_MAX. Returns the new length.
size_t bootlog_add(char *buf, size_t len, const char *line);
// "3 d 4 h" ("3 d" at 0 h), "5 h 12 min" ("5 h" at 0 min), "42 min", "under a minute"
void bootlog_dur(char *out, size_t cap, uint32_t s);
