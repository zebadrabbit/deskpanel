// Host-side check of layout_parse() and layout_equal(): `sh test/run.sh`.
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "layout.h"

static const char doc[] =
    "{\"tabs\":[{\"name\":\"Homelab\",\"cards\":["
    "{\"id\":\"gpu-workstation\",\"type\":\"gpu\",\"at\":[1,1],\"size\":[2,3],\"src\":\"workstation\",\"tap\":\"none\"},"
    "{\"id\":\"gpu-server-2\",\"type\":\"gpu\",\"at\":[3,1],\"size\":[2,3],\"src\":\"server#2\",\"tap\":\"none\"},"
    "{\"id\":\"nas-alerts\",\"type\":\"nas-alerts\",\"at\":[4,4],\"size\":[1,2],\"tap\":\"alert-details\"}]},"
    "{\"name\":\"Planka\",\"cards\":[{\"id\":\"board\",\"type\":\"board\",\"at\":[1,1],\"size\":[4,6],"
    "\"title\":\"Work\",\"tap\":\"none\",\"future\":{\"x\":1}}],\"extra\":true},"
    "{\"name\":\"Weather\",\"cards\":[{\"id\":\"radar\",\"type\":\"radar\",\"at\":[1,1],\"size\":[4,6],\"tap\":\"none\"}]}],"
    "\"version\":9}";

int main(void)
{
    static layout_t l, l2;
    assert(layout_parse(doc, strlen(doc), &l));
    assert(l.ntabs == 3 && !strcmp(l.tabs[0].name, "Homelab") && l.tabs[0].ncards == 3);
    const layout_card_t *c = &l.tabs[0].cards[0];
    assert(!strcmp(c->id, "gpu-workstation") && !strcmp(c->type, "gpu") && !strcmp(c->src, "workstation"));
    assert(c->col == 1 && c->row == 1 && c->w == 2 && c->h == 3 && !strcmp(c->tap, "none") && !c->title[0]);
    assert(!strcmp(l.tabs[0].cards[1].src, "server#2") && !l.tabs[0].cards[2].src[0]);
    assert(!strcmp(l.tabs[0].cards[2].tap, "alert-details"));
    assert(!strcmp(l.tabs[1].cards[0].title, "Work")); // unknown fields ignored

    // equality
    assert(layout_parse(doc, strlen(doc), &l2) && layout_equal(&l, &l2));
    l2.tabs[1].cards[0].h = 5;
    assert(!layout_equal(&l, &l2));

    // not a layout
    assert(!layout_parse("nope", 4, &l) && !layout_parse("[1]", 3, &l) && !layout_parse("{}", 2, &l));
    assert(!layout_parse("{\"tabs\":{}}", 11, &l));
    assert(layout_parse("{\"tabs\":[]}", 11, &l) && l.ntabs == 0);

    // caps: 7 tabs -> 6, 25 cards -> 24
    char *big = malloc(8192), *p = big;
    p += sprintf(p, "{\"tabs\":[");
    for (int i = 0; i < 7; i++) {
        p += sprintf(p, "%s{\"name\":\"t%d\",\"cards\":[", i ? "," : "", i);
        for (int j = 0; j < 25; j++) {
            p += sprintf(p, "%s{\"id\":\"c%d\",\"at\":[1,1],\"size\":[1,1]}", j ? "," : "", j);
        }
        p += sprintf(p, "]}");
    }
    p += sprintf(p, "]}");
    assert(layout_parse(big, strlen(big), &l));
    assert(l.ntabs == LAYOUT_MAX_TABS && l.tabs[5].ncards == LAYOUT_MAX_CARDS);
    assert(!strcmp(l.tabs[5].name, "t5") && !strcmp(l.tabs[0].cards[23].id, "c23"));
    free(big);

    // UTF-8 cut: tab name is 16 bytes (15 usable); 7 x 2-byte chars + 1 byte = 15, an 8th 2-byte char is cut whole
    static const char u[] = "{\"tabs\":[{\"name\":\"\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9\"}]}";
    assert(layout_parse(u, strlen(u), &l) && strlen(l.tabs[0].name) == 14);
    assert(!memcmp(l.tabs[0].name, "\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9", 14));

    // bad at/size (wrong length, wrong type, missing): that card is skipped, the rest kept
    static const char bad[] =
        "{\"tabs\":[{\"name\":\"x\",\"cards\":["
        "{\"id\":\"a\",\"at\":[1],\"size\":[1,1]},{\"id\":\"b\",\"at\":[1,1,1],\"size\":[1,1]},"
        "{\"id\":\"c\",\"at\":\"1,1\",\"size\":[1,1]},{\"id\":\"d\",\"at\":[1,1],\"size\":[\"1\",1]},"
        "{\"id\":\"e\",\"at\":[1,1]},5,{\"id\":\"ok\",\"at\":[2,3],\"size\":[1,1]}]}]}";
    assert(layout_parse(bad, strlen(bad), &l) && l.tabs[0].ncards == 1);
    assert(!strcmp(l.tabs[0].cards[0].id, "ok") && l.tabs[0].cards[0].col == 2 && l.tabs[0].cards[0].row == 3);
    // skipped cards leave no residue behind the count
    assert(!l.tabs[0].cards[1].id[0]);

    // The aggregator's real custom.json: resized cards and a pool with a source that doesn't exist.
    static const char custom[] =
        "{\"tabs\":[{\"name\":\"Homelab\",\"cards\":[{\"id\":\"gpu-workstation\",\"type\":\"gpu\",\"at\":[1,1],\"size\":[1,2],"
        "\"src\":\"workstation\",\"tap\":\"none\"},{\"id\":\"pool-tank\",\"type\":\"pool\",\"at\":[2,1],\"size\":[2,2],"
        "\"src\":\"tank\",\"tap\":\"none\"},{\"id\":\"nas-alerts\",\"type\":\"nas-alerts\",\"at\":[4,1],\"size\":[1,1],"
        "\"tap\":\"alert-details\"},{\"id\":\"dots\",\"type\":\"dots\",\"at\":[1,3],\"size\":[2,1],\"src\":\"both\","
        "\"tap\":\"none\"},{\"id\":\"pool-tnak\",\"type\":\"pool\",\"at\":[1,4],\"size\":[1,1],\"src\":\"tnak\","
        "\"tap\":\"none\"}]},{\"name\":\"Planka\",\"cards\":[{\"id\":\"board\",\"type\":\"board\",\"at\":[1,1],"
        "\"size\":[4,6],\"tap\":\"none\"}]},{\"name\":\"Weather\",\"cards\":[{\"id\":\"radar\",\"type\":\"radar\","
        "\"at\":[1,1],\"size\":[4,6],\"tap\":\"none\"}]}]}";
    assert(layout_parse(custom, strlen(custom), &l) && l.ntabs == 3 && l.tabs[0].ncards == 5);
    assert(l.tabs[0].cards[0].w == 1 && l.tabs[0].cards[0].h == 2);
    assert(l.tabs[0].cards[1].col == 2 && l.tabs[0].cards[1].w == 2 && l.tabs[0].cards[1].h == 2);
    assert(!strcmp(l.tabs[0].cards[4].id, "pool-tnak") && !strcmp(l.tabs[0].cards[4].src, "tnak"));
    assert(l.tabs[0].cards[4].col == 1 && l.tabs[0].cards[4].row == 4);

    // gpu slots: filled when present, empty otherwise; two parses compare equal
    static const char sl[] =
        "{\"tabs\":[{\"name\":\"x\",\"cards\":[{\"id\":\"g\",\"type\":\"gpu\",\"at\":[1,1],\"size\":[2,3],"
        "\"slots\":{\"gauge\":\"temp\",\"line1\":\"vram\",\"as\":\"bar\",\"line2\":[\"util\",\"power\"]}},"
        "{\"id\":\"h\",\"type\":\"gpu\",\"at\":[3,1],\"size\":[1,1],\"slots\":{\"gauge\":\"util\",\"line2\":[]}},"
        "{\"id\":\"n\",\"type\":\"nas-alerts\",\"at\":[1,4],\"size\":[1,1]}]}]}";
    assert(layout_parse(sl, strlen(sl), &l) && layout_parse(sl, strlen(sl), &l2) && layout_equal(&l, &l2));
    const layout_card_t *s = l.tabs[0].cards;
    assert(!strcmp(s[0].gauge, "temp") && !strcmp(s[0].line1, "vram") && !strcmp(s[0].as, "bar"));
    assert(!strcmp(s[0].line2[0], "util") && !strcmp(s[0].line2[1], "power"));
    assert(!strcmp(s[1].gauge, "util") && !s[1].line1[0] && !s[1].as[0] && !s[1].line2[0][0]);
    assert(!s[2].gauge[0] && !s[2].line1[0] && !s[2].as[0] && !s[2].line2[0][0] && !s[2].line2[1][0]);

    // tour block: absent -> today's behaviour
    assert(layout_parse("{\"tabs\":[]}", 11, &l));
    assert(l.tour_on && l.tour_idle_ms == 300000 && l.tour_clock_ms == 60000 && l.tour_tab_ms == 20000);
    assert(l.tour_clock && l.tour_skip == 0);
#define PARSE(s) (layout_parse(s, strlen(s), &l))
    assert(PARSE("{\"tabs\":[],\"tour\":{\"on\":false,\"idle_s\":120,\"clock_s\":15,\"tab_s\":30,\"clock\":false,"
                 "\"skip\":[1,3]}}"));
    assert(!l.tour_on && l.tour_idle_ms == 120000 && l.tour_clock_ms == 15000 && l.tour_tab_ms == 30000);
    assert(!l.tour_clock && l.tour_skip == 0x0A);
    // out of range clamps; a bad skip entry is ignored
    assert(PARSE("{\"tabs\":[],\"tour\":{\"idle_s\":5,\"tab_s\":100000,\"clock_s\":1,\"skip\":[9,-1,\"x\",2]}}"));
    assert(l.tour_idle_ms == 60000 && l.tour_tab_ms == 600000 && l.tour_clock_ms == 5000 && l.tour_skip == 0x04);
    assert(PARSE("{\"tabs\":[],\"tour\":{\"idle_s\":99999}}") && l.tour_idle_ms == 7200000);
    // wrong types -> defaults, layout still parses
    assert(PARSE("{\"tabs\":[{\"name\":\"x\"}],\"tour\":{\"on\":\"no\",\"idle_s\":\"60\",\"clock_s\":null,\"tab_s\":[1],"
                 "\"clock\":0,\"skip\":3}}") && l.ntabs == 1);
    assert(l.tour_on && l.tour_idle_ms == 300000 && l.tour_clock_ms == 60000 && l.tour_tab_ms == 20000);
    assert(l.tour_clock && l.tour_skip == 0);
    assert(PARSE("{\"tabs\":[],\"tour\":5}") && l.tour_on && l.tour_idle_ms == 300000);

    puts("layout_test: ok");
    return 0;
}
