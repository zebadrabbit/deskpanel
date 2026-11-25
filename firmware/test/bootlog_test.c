// Host-side check of bootlog_add() and bootlog_dur(): `sh test/run.sh`.
#include <assert.h>
#include <stdio.h>
#include <string.h>

#include "bootlog.h"

static void dur(uint32_t s, const char *want)
{
    char o[32];
    bootlog_dur(o, sizeof o, s);
    if (strcmp(o, want)) {
        printf("bootlog_dur(%u) = \"%s\", want \"%s\"\n", (unsigned)s, o, want);
        assert(0);
    }
}

int main(void)
{
    dur(0, "under a minute");
    dur(59, "under a minute");
    dur(60, "1 min");
    dur(3599, "59 min");
    dur(3600, "1 h");
    dur(5 * 3600 + 12 * 60, "5 h 12 min");
    dur(86400, "1 d");
    dur(3 * 86400 + 4 * 3600 + 59, "3 d 4 h");

    static char buf[BOOTLOG_MAX];
    size_t n = bootlog_add(buf, 0, "a\n");
    assert(n == 2 && !memcmp(buf, "a\n", 2));
    memcpy(buf, "x", 1);
    n = bootlog_add(buf, 1, "b\n");
    assert(n == 4 && !memcmp(buf, "x\nb\n", 4));

    char line[101];
    n = 0;
    for (int i = 1; i <= 200; i++) {
        snprintf(line, sizeof line, "%099d\n", i); // 99 digits + \n = 100 bytes
        n = bootlog_add(buf, n, line);
        assert(n <= BOOTLOG_MAX);
    }
    assert(n == 16300);
    snprintf(line, sizeof line, "%099d\n", 38);
    assert(!memcmp(buf, line, 100));
    snprintf(line, sizeof line, "%099d\n", 200);
    assert(!memcmp(buf + n - 100, line, 100));
    printf("bootlog_test ok\n");
    return 0;
}
