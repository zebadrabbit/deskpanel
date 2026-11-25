// Host-side check of weather_parse(), weather_alert_text(), wx_markup() and the takeover acks: `sh test/run.sh`.
#include <assert.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "panel.h"
#include "weather.h"

// The aggregator demo's tornado-warning document, plus a 5th..6th alert to test the cap.
static const char doc[] =
    "{\"alerts\":{\"list\":[{\"n\":0,\"key\":\"demo0001\",\"event\":\"Tornado Warning\",\"cls\":\"warning\","
    "\"until\":1794991563,\"take\":true,\"lvl\":2},"
    "{\"n\":1,\"key\":\"demo0002\",\"event\":\"Tornado Watch\",\"cls\":\"watch\",\"until\":1795010883},"
    "{\"n\":2,\"key\":\"demo0003\",\"event\":\"Wind Advisory\",\"cls\":\"advisory\",\"until\":1795003683},"
    "{\"n\":3,\"key\":\"k3\"},{\"n\":4,\"key\":\"k4\"},{\"n\":5,\"key\":\"k5\"}],"
    "\"polys\":[{\"k\":\"t\",\"h\":true,\"p\":[[350,150],[450,130],[475,210],[400,245],[340,215]]},"
    "{\"k\":\"s\",\"p\":[[190,140],[300,130],[320,200],[250,240],[180,210]]}]},"
    "\"frames\":[1794987900,1794988200,1794988500,1794988800,1794989100],"
    "\"now\":{\"t\":72,\"rh\":64,\"code\":2,\"day\":true,\"text\":\"Partly cloudy\",\"hi\":78,\"lo\":61,"
    "\"wind\":\"S 14 mph\"},\"stale\":false,\"updated_at\":1794989283}";

static const char quiet[] = "{\"alerts\":{\"list\":[{\"n\":0,\"key\":\"a\",\"event\":\"Wind Advisory\"}],\"polys\":[]},"
                            "\"frames\":[],\"now\":{\"t\":50},\"stale\":true,\"updated_at\":5}";

static const char alert[] =
    "{\"event\":\"Tornado Warning\",\"headline\":\"Tornado Warning issued until 8:46 AM\",\"sent\":1794989223,"
    "\"expires\":1794991563,\"desc\":\"A storm.\\n\\nHAZARD... Tornado.\",\"instr\":\"TAKE COVER NOW!\"}";

int main(void)
{
    weather_t w;
    assert(weather_parse(doc, strlen(doc), &w));
    assert(!w.stale && w.has_now && w.updated_at == 1794989283);
    assert(w.now.t == 72 && w.now.rh == 64 && w.now.code == 2 && w.now.day && w.now.hi == 78 && w.now.lo == 61);
    assert(!strcmp(w.now.text, "Partly cloudy") && !strcmp(w.now.wind, "S 14 mph"));
    assert(w.nframes == 5 && w.frames[0] == 1794987900 && w.frames[4] == 1794989100);
    assert(w.nalerts == WX_MAX_ALERTS); // 6 in the document, 4 kept
    assert(w.alerts[0].take && w.alerts[0].lvl == 2 && !strcmp(w.alerts[0].key, "demo0001"));
    assert(!strcmp(w.alerts[0].event, "Tornado Warning") && !strcmp(w.alerts[0].cls, "warning"));
    assert(w.alerts[0].until == 1794991563 && !w.alerts[1].take && w.alerts[1].n == 1);
    assert(w.npolys == 2 && w.polys[0].kind == 't' && w.polys[0].home && w.polys[0].npts == 5);
    assert(w.polys[0].pts[1][0] == 450 && w.polys[0].pts[1][1] == 130);
    assert(w.polys[1].kind == 's' && !w.polys[1].home);

    // Before the aggregator's first collection the pieces can be null.
    static const char nulls[] = "{\"alerts\":null,\"frames\":null,\"now\":null,\"stale\":true}";
    assert(weather_parse(nulls, strlen(nulls), &w));
    assert(!w.has_now && w.nframes == 0 && w.nalerts == 0 && w.npolys == 0 && w.stale);
    static const char nulls2[] = "{\"alerts\":{\"list\":null,\"polys\":null}}";
    assert(weather_parse(nulls2, strlen(nulls2), &w) && w.nalerts == 0 && w.npolys == 0);
    assert(!weather_parse("not json", 8, &w) && !weather_parse("[1]", 3, &w));

    // Too many frames / points are capped.
    static const char many[] = "{\"frames\":[1,2,3,4,5,6,7],\"alerts\":{\"polys\":[{\"k\":\"t\",\"p\":["
                               "[1,1],[2,2],[3,3],[4,4],[5,5],[6,6],[7,7],[8,8],[9,9],[10,10],[11,11],[12,12],"
                               "[13,13],[14,14],[15,15],[16,16],[17,17],[18,18]]}]}}";
    assert(weather_parse(many, strlen(many), &w));
    assert(w.nframes == WX_FRAMES && w.polys[0].npts == WX_MAX_PTS);

    // Takeover: first take alert not yet acknowledged at its level.
    weather_t q;
    assert(weather_parse(doc, strlen(doc), &w) && weather_parse(quiet, strlen(quiet), &q));
    wx_acks_t a = {0};
    assert(wx_takeover_due(&w, &a) == 0);
    wx_ack(&a, "demo0001", 2);
    assert(wx_takeover_due(&w, &a) == -1);
    w.alerts[0].lvl = 3; // escalated
    assert(wx_takeover_due(&w, &a) == 0);
    wx_ack(&a, "demo0001", 3);
    assert(wx_takeover_due(&w, &a) == -1 && a.n == 1); // raised in place
    assert(wx_takeover_due(&q, &a) == -1);
    for (int i = 0; i < 10; i++) { // full: evict the oldest
        char k[12];
        snprintf(k, sizeof k, "k%d", i);
        wx_ack(&a, k, 1);
    }
    assert(a.n == 8 && wx_takeover_due(&w, &a) == 0); // demo0001 was the oldest, now forgotten

    // Alert text, in local time (pinned to UTC here): four parts, NUL-separated in out.
    setenv("TZ", "UTC", 1);
    tzset();
    char out[512];
    wx_text_t t;
    assert(weather_alert_text(alert, strlen(alert), out, sizeof out, &t));
    assert(!strcmp(t.head, "Tornado Warning issued until 8:46 AM") &&
           !strcmp(t.when, "Issued 8:07 AM \xC2\xB7 expires 8:46 AM"));
    assert(!strcmp(t.desc, "A storm.\n\nHAZARD... Tornado.") && !strcmp(t.instr, "TAKE COVER NOW!"));
    // Every cap: cut as a whole, every part and its NUL inside b, never a half middle dot at a part's end.
    for (size_t c = 1; c <= 40; c++) {
        char b[40];
        assert(weather_alert_text(alert, strlen(alert), b, c, &t));
        const char *part[] = {t.head, t.when, t.desc, t.instr};
        size_t bytes = 0;
        for (int k = 0; k < 4; k++) {
            size_t n = strlen(part[k]);
            assert(part[k] >= b && part[k] + n < b + c);
            assert(!n || (unsigned char)part[k][n - 1] != 0xC2);
            bytes += n;
        }
        assert(c < 4 ? !bytes : bytes + 4 <= c); // (under 4 bytes every part is "")
        assert(!t.when[0] || !strcmp(t.head, "Tornado Warning issued until 8:46 AM"));
    }
    assert(!weather_alert_text("nope", 4, out, sizeof out, &t));
    static const char nosent[] = "{\"headline\":\"H\",\"expires\":1794991563,\"desc\":\"D\",\"instr\":\"I\"}";
    static const char notimes[] = "{\"headline\":\"H\",\"sent\":-62135596800,\"desc\":\"D\",\"instr\":\"I\"}";
    assert(weather_alert_text(nosent, strlen(nosent), out, sizeof out, &t) && !strcmp(t.when, "Expires 8:46 AM"));
    assert(!strcmp(t.head, "H") && !strcmp(t.desc, "D") && !strcmp(t.instr, "I"));
    assert(weather_alert_text(notimes, strlen(notimes), out, sizeof out, &t) && !strcmp(t.when, ""));
    assert(!strcmp(t.head, "H") && !strcmp(t.desc, "D") && !strcmp(t.instr, "I"));

    // A paragraph as LVGL recolor text: a leading keyword in yellow, every '#' doubled.
    static const char *const mark[][2] = {
        {"HAZARD... Tornado.", "#ffd35a HAZARD...# Tornado."},
        {"* WHAT... Wind.", "* #ffd35a WHAT...# Wind."},
        {"PRECAUTIONARY/PREPAREDNESS ACTIONS... Go.", "#ffd35a PRECAUTIONARY/PREPAREDNESS ACTIONS...# Go."},
        {"At 2:52 PM, a storm", "At 2:52 PM, a storm"},
        {"Route #1 closed", "Route ##1 closed"},
    };
    char m[128];
    for (size_t i = 0; i < sizeof mark / sizeof mark[0]; i++) {
        assert(wx_markup(mark[i][0], strlen(mark[i][0]), m, sizeof m) == strlen(mark[i][1]) && !strcmp(m, mark[i][1]));
    }
    assert(wx_markup("A storm.\n\nHAZARD...", 8, m, sizeof m) == 8 && !strcmp(m, "A storm.")); // len bounds it
    wx_alert_t tor = {.until = 1794991563, .event = "Tornado Warning"};
    assert(weather_alert_matches(alert, strlen(alert), &tor));
    tor.until++;
    assert(!weather_alert_matches(alert, strlen(alert), &tor)); // another warning, or extended since the chip was drawn
    tor.until--;
    strcpy(tor.event, "Tornado Watch");
    assert(!weather_alert_matches(alert, strlen(alert), &tor));
    assert(!weather_alert_matches("nope", 4, &tor));
    // With keys on both sides the key decides, however the end time or text drifted.
    static const char keyed[] = "{\"key\":\"demo0001\",\"event\":\"Tornado Warning\",\"expires\":1794991599}";
    strcpy(tor.event, "Tornado Warning");
    strcpy(tor.key, "demo0001");
    assert(weather_alert_matches(keyed, strlen(keyed), &tor));
    strcpy(tor.key, "demo0002");
    assert(!weather_alert_matches(keyed, strlen(keyed), &tor));
    tor.key[0] = '\0'; // no key on the chip: event and end time, as before
    assert(!weather_alert_matches(keyed, strlen(keyed), &tor));
    assert(weather_alert_matches(alert, strlen(alert), &tor)); // an unkeyed text too

    // Badge and notice wording: an escalation must not look like the warning already acknowledged.
    static const char *const want[][2] = {
        {"TORNADO WARNING \xC2\xB7 until 3:30 PM", "Tornado Warning for your area until 3:30 PM. Take shelter now."},
        {"TORNADO OBSERVED \xC2\xB7 until 3:30 PM",
         "Tornado Warning for your area until 3:30 PM \xE2\x80\x94 tornado observed. Take shelter now."},
        {"DANGEROUS TORNADO \xC2\xB7 until 3:30 PM",
         "Tornado Warning for your area until 3:30 PM \xE2\x80\x94 considerable damage threat. Take shelter now."},
        {"TORNADO EMERGENCY \xC2\xB7 until 3:30 PM", "TORNADO EMERGENCY for your area until 3:30 PM. Take shelter now."},
    };
    wx_alert_t al = {.event = "Tornado Warning"};
    char badge[64], notice[160];
    for (int l = 0; l < 4; l++) {
        al.lvl = l;
        wx_badge_text(&al, "until 3:30 PM", badge, sizeof badge);
        wx_notice_text(&al, "until 3:30 PM", notice, sizeof notice);
        assert(!strcmp(badge, want[l][0]) && !strcmp(notice, want[l][1]));
    }
    al.lvl = 9; // out of range: the highest
    wx_badge_text(&al, "", badge, sizeof badge);
    wx_notice_text(&al, "", notice, sizeof notice);
    assert(!strcmp(badge, "TORNADO EMERGENCY") && !strcmp(notice, "TORNADO EMERGENCY for your area. Take shelter now."));
    al.lvl = 1;
    wx_notice_text(&al, "", notice, sizeof notice);
    assert(!strcmp(notice, "Tornado Warning for your area \xE2\x80\x94 tornado observed. Take shelter now."));
    wx_alert_t wind = {.event = "Extreme Wind Warning", .lvl = 2}; // levels are for tornadoes only
    wx_badge_text(&wind, "until 3:30 PM", badge, sizeof badge);
    wx_notice_text(&wind, "until 3:30 PM", notice, sizeof notice);
    assert(!strcmp(badge, "EXTREME WIND WARNING \xC2\xB7 until 3:30 PM"));
    assert(!strcmp(notice, "Extreme Wind Warning for your area until 3:30 PM. Take shelter now."));
    wx_alert_t adv = {.event = "Wind Advisory"};
    wx_badge_text(&adv, "until 9:00 PM", badge, sizeof badge);
    assert(!strcmp(badge, "WIND ADVISORY \xC2\xB7 until 9:00 PM"));

    // Frame sets: an empty set replaces a shown one only when every buffer is busy (Review Focus 5).
    assert(wx_frames_publish(1, 0x1f, 0x1f, 6));  // a new frame landed
    assert(!wx_frames_publish(0, 0x1f, 0x1f, 6)); // nothing new yet, buffer 5 free: keep the old loop
    assert(wx_frames_publish(0, 0x1f, 0x1f, 5));  // 5-buffer fallback, all busy: let them go so they can refill
    assert(wx_frames_publish(0, 0x0f, 0x1f, 5));  // busy_mask counts: the UI still holds buffer 4
    assert(!wx_frames_publish(0, 0x0f, 0x0f, 5)); // buffer 4 is free: keep the old loop
    assert(wx_frames_publish(0, 0, 0, 6));        // nothing shown: nothing to keep

    // Notes carry an action.
    static const char note[] = "{\"notify\":{\"notes\":[{\"id\":1,\"msg\":\"m\",\"act\":\"wx\"}]}}";
    panel_t p;
    assert(panel_parse(note, strlen(note), &p) && p.nnotes == 1 && !strcmp(p.notes[0].act, "wx"));

    puts("weather_test: ok");
    return 0;
}
