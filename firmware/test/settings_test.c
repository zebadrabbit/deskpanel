// Host-side check of settings_parse(): `sh test/run.sh`.
#include <assert.h>
#include <stdio.h>
#include <string.h>

#include "settings.h"

static char warned[2048];
static int nwarn;

static void warn(const char *msg)
{
    strncat(warned, msg, sizeof warned - strlen(warned) - 1);
    strncat(warned, "\n", sizeof warned - strlen(warned) - 1);
    nwarn++;
}

static settings_t base(void)
{
    return (settings_t){"OldNet", "oldpass", "http://192.0.2.1:8088", "oldtoken", "UTC0", "pool.ntp.org"};
}

// Parses text from a buffer with no NUL after it (junk instead), so a parser that reads past len shows.
static unsigned parse(settings_t *s, const char *text)
{
    static char buf[4096];
    size_t n = strlen(text);
    assert(n + 3 < sizeof buf);
    memset(buf, 'Z', sizeof buf);
    memcpy(buf, text, n);
    buf[n + 3] = '\n'; // a strchr-style search would stop here, taking "ZZZ" into the last value
    warned[0] = 0;
    nwarn = 0;
    return settings_parse(s, buf, n, warn);
}

int main(void)
{
    static const char *const names[SETTINGS_KEYS] = {"WIFI_SSID", "WIFI_PASSWORD", "AGGREGATOR_URL", "PANEL_TOKEN",
                                                     "DESKPANEL_TZ", "NTP_SERVER"};
    for (int i = 0; i < SETTINGS_KEYS; i++) {
        assert(!strcmp(settings_key[i], names[i]));
    }

    settings_t s = base(), b = base();
    assert(parse(&s, "") == 0 && !memcmp(&s, &b, sizeof s) && nwarn == 0);

    // The full file: BOM, comment, CRLF, spaces around '=', '#' and '=' in a value, a trailing '/', no last '\n'.
    s = base();
    assert(parse(&s, "\xEF\xBB\xBF# deskpanel settings\r\n\r\nWIFI_SSID = My Network\r\nWIFI_PASSWORD=pa ss#word=1\r\n"
                     "  AGGREGATOR_URL=http://192.0.2.20:8088/\nPANEL_TOKEN=abc123\nDESKPANEL_TZ=CET-1CEST,M3.5.0,M10.5.0/3\n"
                     "NTP_SERVER=time.example.com") == 0x3f);
    assert(nwarn == 0);
    assert(!strcmp(s.wifi_ssid, "My Network"));
    assert(!strcmp(s.wifi_password, "pa ss#word=1"));
    assert(!strcmp(s.aggregator_url, "http://192.0.2.20:8088"));
    assert(!strcmp(s.panel_token, "abc123"));
    assert(!strcmp(s.tz, "CET-1CEST,M3.5.0,M10.5.0/3"));
    assert(!strcmp(s.ntp_server, "time.example.com"));

    // Problems: each ignored with a warning that names no value; the last good line for a key wins.
    char text[1024], xs[96];
    memset(xs, 'x', 95);
    xs[95] = 0;
    snprintf(text, sizeof text,
             "WIFI_SSD=x\nhunter2\nPANEL_TOKEN=s3cr3t%s\nAGGREGATOR_URL=192.0.2.20:8088\nAGGREGATOR_URL=https://192.0.2.20\n"
             "AGGREGATOR_URL=http://a b\nWIFI_SSID=\n# PANEL_TOKEN=commented\nWIFI_SSID=Second\n", xs);
    s = base();
    assert(parse(&s, text) == 1u << 0);
    assert(!strcmp(s.wifi_ssid, "Second"));
    assert(!strcmp(s.panel_token, "oldtoken") && !strcmp(s.aggregator_url, "http://192.0.2.1:8088"));
    assert(nwarn == 6);
    assert(strstr(warned, "settings.txt line 1: unknown key WIFI_SSD"));
    assert(strstr(warned, "line 2: no '='"));
    assert(strstr(warned, "line 3: PANEL_TOKEN is over 95"));
    assert(strstr(warned, "line 4: AGGREGATOR_URL must be"));
    assert(!strstr(warned, "hunter2") && !strstr(warned, "s3cr3t"));

    // A URL with nothing after "http://", even once its trailing '/' are gone, is no URL.
    s = base();
    assert(parse(&s, "AGGREGATOR_URL=http://\nAGGREGATOR_URL=http:///\n") == 0 && nwarn == 2);
    assert(!strcmp(s.aggregator_url, "http://192.0.2.1:8088"));

    // URLs esp_http_client couldn't parse (which would stop the polls) are refused here: no host, a port that isn't
    // 1..65535 in 1-5 digits, odd host characters, or anything but a path after host:port.
    s = base();
    assert(parse(&s, "AGGREGATOR_URL=http://host:99999\nAGGREGATOR_URL=http://:8088\nAGGREGATOR_URL=http://host:80a\n"
                     "AGGREGATOR_URL=http://host:abc\nAGGREGATOR_URL=http://host:\nAGGREGATOR_URL=http://host:0\n"
                     "AGGREGATOR_URL=http://host:065536\nAGGREGATOR_URL=http://host:123456\n"
                     "AGGREGATOR_URL=http://us@host:8088\nAGGREGATOR_URL=http://host?x\n") == 0);
    assert(nwarn == 10 && !strcmp(s.aggregator_url, "http://192.0.2.1:8088"));
    assert(parse(&s, "AGGREGATOR_URL=http://my-server.lan:65535/deskpanel/") == 1u << 2 && nwarn == 0);
    assert(!strcmp(s.aggregator_url, "http://my-server.lan:65535/deskpanel"));
    assert(parse(&s, "AGGREGATOR_URL=http://192.0.2.20") == 1u << 2 && !strcmp(s.aggregator_url, "http://192.0.2.20"));

    // A prefix of a real key is not that key.
    s = b = base();
    assert(parse(&s, "WIFI=x") == 0 && nwarn == 1 && !memcmp(&s, &b, sizeof s));
    assert(strstr(warned, "unknown key WIFI, ignored"));

    // An unknown key is named only when it looks like one (capitals, digits, '_'): a malformed line can hold a value.
    assert(parse(&s, "WIFI_PASSWORD: pa=ss\nabc=def\n=x\n") == 0 && nwarn == 3 && !memcmp(&s, &b, sizeof s));
    assert(!strcmp(warned, "settings.txt line 1: unknown key, ignored\nsettings.txt line 2: unknown key, ignored\n"
                           "settings.txt line 3: unknown key, ignored\n"));

    // Spaces and tabs after a value are dropped.
    assert(parse(&s, "PANEL_TOKEN=abc \t") == 1u << 3 && !strcmp(s.panel_token, "abc"));

    // Limits: the field size less the NUL.
    s = base();
    assert(parse(&s, "WIFI_SSID=1234567890123456789012345678901") == 1u << 0 && nwarn == 0);
    assert(strlen(s.wifi_ssid) == 31);
    assert(parse(&s, "WIFI_SSID=12345678901234567890123456789012") == 0 && nwarn == 1);
    assert(strlen(s.wifi_ssid) == 31);
    assert(parse(&s, "WIFI_PASSWORD=123456789012345678901234567890123456789012345678901234567890123") == 1u << 1);
    assert(nwarn == 0 && strlen(s.wifi_password) == 63);
    assert(parse(&s, "WIFI_PASSWORD=1234567890123456789012345678901234567890123456789012345678901234") == 0);
    assert(nwarn == 1 && strlen(s.wifi_password) == 63);

    // WIFI_SSID= alone: "" (Wi-Fi off, as in secrets.example.h).
    s = base();
    assert(parse(&s, "WIFI_SSID=") == 1u << 0 && s.wifi_ssid[0] == 0 && nwarn == 0);

    printf("settings_test ok\n");
    return 0;
}
