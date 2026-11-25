#include "settings.h"

#include <stdio.h>
#include <string.h>

const char *const settings_key[SETTINGS_KEYS] = {"WIFI_SSID", "WIFI_PASSWORD", "AGGREGATOR_URL", "PANEL_TOKEN",
                                                 "DESKPANEL_TZ", "NTP_SERVER"};

#define FIELD(f) {offsetof(settings_t, f), sizeof ((settings_t *)0)->f}
static const struct { size_t off, cap; } field[SETTINGS_KEYS] = {
    FIELD(wifi_ssid), FIELD(wifi_password), FIELD(aggregator_url), FIELD(panel_token), FIELD(tz), FIELD(ntp_server)};
#define URL_KEY 2 // AGGREGATOR_URL

static int blank(char c) { return c == ' ' || c == '\t'; }
static int digit(char c) { return c >= '0' && c <= '9'; }
static int host_char(char c) // http_parser's, '_' included
{
    return digit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '.' || c == '-' || c == '_';
}

// "http://" host [":" port 1..65535] [path], the path printable ASCII but space. A URL esp_http_client can't parse
// would stop the polls (net_task), so a doubtful one is refused here and the built-in URL stays.
static int url_ok(const char *v, size_t n)
{
    if (n <= 7 || memcmp(v, "http://", 7)) {
        return 0;
    }
    size_t i = 7;
    while (i < n && host_char(v[i])) {
        i++;
    }
    if (i == 7) { // no host
        return 0;
    }
    if (i < n && v[i] == ':') {
        size_t d = ++i;
        unsigned port = 0;
        for (; i < n && digit(v[i]); i++) {
            if (i - d < 5) {
                port = port * 10 + (unsigned)(v[i] - '0');
            }
        }
        if (i == d || i - d > 5 || port < 1 || port > 65535) {
            return 0;
        }
    }
    if (i < n && v[i] != '/') { // after host:port, only a path
        return 0;
    }
    for (; i < n; i++) {
        if (v[i] <= ' ' || v[i] > '~') {
            return 0;
        }
    }
    return 1;
}

// Only a key-like word is echoed in a warning: a malformed line ("WIFI_PASSWORD: pa=ss") can hold part of a value.
static int key_like(const char *k, size_t n)
{
    for (size_t i = 0; i < n; i++) {
        if (!digit(k[i]) && !(k[i] >= 'A' && k[i] <= 'Z') && k[i] != '_') {
            return 0;
        }
    }
    return n > 0;
}

unsigned settings_parse(settings_t *s, const char *text, size_t len, void (*warn)(const char *msg))
{
    if (len >= 3 && !memcmp(text, "\xEF\xBB\xBF", 3)) { // Notepad's UTF-8 BOM
        text += 3;
        len -= 3;
    }
    unsigned got = 0, line = 0;
    const char *p = text, *end = text + len;
    while (p < end) {
        const char *nl = memchr(p, '\n', end - p), *e = nl ? nl : end, *next = nl ? nl + 1 : end;
        line++;
        if (e > p && e[-1] == '\r') {
            e--;
        }
        while (p < e && blank(*p)) {
            p++;
        }
        while (e > p && blank(e[-1])) {
            e--;
        }
        if (p == e || *p == '#') { // blank or a comment
            p = next;
            continue;
        }
        const char *eq = memchr(p, '=', e - p);
        char msg[128];
        int pre = snprintf(msg, sizeof msg, "settings.txt line %u: ", line);
        if (!eq) {
            snprintf(msg + pre, sizeof msg - pre, "no '=', ignored");
            warn(msg);
            p = next;
            continue;
        }
        const char *ke = eq, *v = eq + 1;
        while (ke > p && blank(ke[-1])) {
            ke--;
        }
        while (v < e && blank(*v)) {
            v++;
        }
        size_t klen = ke - p, vlen = e - v;
        int i = 0;
        while (i < SETTINGS_KEYS && (strlen(settings_key[i]) != klen || memcmp(settings_key[i], p, klen))) {
            i++;
        }
        if (i == URL_KEY) {
            while (vlen > 0 && v[vlen - 1] == '/') {
                vlen--;
            }
        }
        if (i == SETTINGS_KEYS && key_like(p, klen)) {
            snprintf(msg + pre, sizeof msg - pre, "unknown key %.*s, ignored", klen > 32 ? 32 : (int)klen, p);
            warn(msg);
        } else if (i == SETTINGS_KEYS) {
            snprintf(msg + pre, sizeof msg - pre, "unknown key, ignored");
            warn(msg);
        } else if (vlen >= field[i].cap) {
            snprintf(msg + pre, sizeof msg - pre, "%s is over %u characters, ignored", settings_key[i],
                     (unsigned)(field[i].cap - 1));
            warn(msg);
        } else if (i == URL_KEY && !url_ok(v, vlen)) {
            snprintf(msg + pre, sizeof msg - pre, "AGGREGATOR_URL must be http://host:port with no spaces, ignored");
            warn(msg);
        } else {
            char *dst = (char *)s + field[i].off;
            memcpy(dst, v, vlen);
            dst[vlen] = 0;
            got |= 1u << i;
        }
        p = next;
    }
    return got;
}
