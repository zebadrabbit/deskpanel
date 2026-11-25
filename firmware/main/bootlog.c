#include "bootlog.h"

#include <stdio.h>
#include <string.h>

size_t bootlog_add(char *buf, size_t len, const char *line)
{
    size_t n = strlen(line);
    size_t nl = len > 0 && buf[len - 1] != '\n'; // a power cut can leave the last line unfinished
    while (len > 0 && len + nl + n > BOOTLOG_MAX) { // drop the oldest whole line
        char *e = memchr(buf, '\n', len);
        size_t cut = e ? (size_t)(e - buf) + 1 : len; // no '\n' at all: the whole (partial) text goes
        memmove(buf, buf + cut, len - cut);
        len -= cut;
        nl = len > 0 && buf[len - 1] != '\n';
    }
    if (nl) {
        buf[len++] = '\n';
    }
    memcpy(buf + len, line, n);
    return len + n;
}

void bootlog_dur(char *out, size_t cap, uint32_t s)
{
    uint32_t d = s / 86400, h = s % 86400 / 3600, m = s % 3600 / 60;
    if (d) {
        if (h) {
            snprintf(out, cap, "%u d %u h", (unsigned)d, (unsigned)h);
        } else {
            snprintf(out, cap, "%u d", (unsigned)d);
        }
    } else if (h) {
        if (m) {
            snprintf(out, cap, "%u h %u min", (unsigned)h, (unsigned)m);
        } else {
            snprintf(out, cap, "%u h", (unsigned)h);
        }
    } else if (m) {
        snprintf(out, cap, "%u min", (unsigned)m);
    } else {
        snprintf(out, cap, "under a minute");
    }
}
