// Host-side check of panel_parse() (no board needed): `sh test/run.sh`.
// With a file argument it also parses that file and prints a summary, e.g. a live /api/v1/panel.
#include <assert.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "panel.h"

static const char sample[] =
    "{\"homelab\":{\"updated_at\":1794964599,\"stale\":true,"
    "\"nas\":{\"pools\":[{\"n\":\"tank\",\"h\":\"ONLINE\",\"ok\":true,\"used\":5464,\"avail\":1847},"
    "{\"n\":\"boot-pool\",\"h\":\"DEGRADED\",\"ok\":false,\"used\":22,\"avail\":128}],"
    "\"alerts\":[{\"lvl\":\"CRITICAL\",\"msg\":\"Pool tank state is DEGRADED\"}],\"info\":3},"
    "\"gpu\":[{\"host\":\"workstation\",\"n\":\"RTX 5060 Ti\",\"up\":true,\"temp\":49,\"util\":12,\"vram\":2429,"
    "\"vram_t\":16311,\"pwr\":17,\"procs\":0,\"st\":\"idle\"},{\"host\":\"truenas\",\"n\":\"\",\"up\":false,\"st\":\"off\"}],"
    "\"ts\":[{\"n\":\"server\",\"on\":true},{\"n\":\"phone\",\"on\":false}],"
    "\"svc\":[{\"n\":\"jellyfin\",\"on\":true}]},"
    "\"planka\":{\"updated_at\":1794964599,\"stale\":false,\"boards\":["
    "{\"id\":\"1910683732176537016\",\"n\":\"Deskpanel\",\"can_done\":true,\"cards\":["
    "{\"id\":\"1910683736010130884\",\"n\":\"Planka page \\u2014 mark done\",\"l\":\"Phase 4 \\u2014 Planka\","
    "\"due\":1794999999,\"lb\":[{\"n\":\"Needs Panda\",\"c\":15569443},{\"n\":\"x\",\"c\":1},{\"n\":\"y\",\"c\":2}]},"
    "{\"id\":\"1910683737805293004\",\"n\":\"Done one\",\"l\":\"Done\",\"d\":true}]},"
    "{\"id\":\"2\",\"n\":\"No done list\",\"can_done\":false,\"cards\":[]}]},"
    "\"notify\":{\"notes\":[{\"id\":1794974846,\"t\":1794974846,\"src\":\"Claude\",\"msg\":\"Claude wants your attention\","
    "\"lvl\":\"info\"},{\"id\":1794974800,\"t\":1794974800,\"src\":\"x\",\"msg\":\"old\",\"lvl\":\"alert\",\"r\":true}]}}";

static bool valid_utf8(const char *s)
{
    for (const unsigned char *u = (const unsigned char *)s; *u;) {
        int n = *u < 0x80 ? 0 : (*u & 0xE0) == 0xC0 ? 1 : (*u & 0xF0) == 0xE0 ? 2 : (*u & 0xF8) == 0xF0 ? 3 : -1;
        if (n < 0) {
            return false;
        }
        for (u++; n > 0; n--, u++) {
            if ((*u & 0xC0) != 0x80) {
                return false;
            }
        }
    }
    return true;
}

int main(int argc, char **argv)
{
    panel_t p;
    assert(panel_parse(sample, strlen(sample), &p));
    assert(p.homelab_stale && p.info_alerts == 3);
    assert(p.npools == 2 && !strcmp(p.pools[1].name, "boot-pool") && !p.pools[1].ok && p.pools[0].used_gib == 5464);
    assert(p.nalerts == 1 && !strcmp(p.alerts[0].level, "CRITICAL"));
    assert(p.ngpus == 2 && p.gpus[0].vram_total == 16311 && !strcmp(p.gpus[0].state, "idle"));
    assert(!p.gpus[1].up && !strcmp(p.gpus[1].state, "off") && p.gpus[1].temp == 0);
    assert(p.npeers == 2 && p.peers[0].on && !p.peers[1].on);
    assert(p.nsvcs == 1 && !strcmp(p.svcs[0].name, "jellyfin") && p.svcs[0].on);
    assert(p.nnotes == 2 && p.notes[0].id == 1794974846 && !p.notes[0].read && !strcmp(p.notes[0].src, "Claude"));
    assert(p.notes[1].read && !strcmp(p.notes[1].lvl, "alert") && p.notes[1].t == 1794974800);
    assert(!p.planka_stale && p.nboards == 2 && p.boards[0].can_done && !p.boards[1].can_done);
    const panel_card_t *k = &p.boards[0].cards[0];
    assert(p.boards[0].ncards == 2 && !strcmp(k->id, "1910683736010130884") && !k->done && k->due == 1794999999);
    assert(!strcmp(k->name, "Planka page \xE2\x80\x94 mark done")); // em dash survives as UTF-8
    assert(k->nlabels == PANEL_MAX_LABELS && k->labels[0].color == 0xed9223);
    assert(p.boards[0].cards[1].done && p.boards[0].cards[1].due == 0 && p.boards[1].ncards == 0);

    // Layout sources: a GPU by host ("#n" = that host's n-th GPU), a pool by name; unknown = NULL.
    assert(panel_gpu(&p, "workstation") == &p.gpus[0] && panel_gpu(&p, "truenas") == &p.gpus[1]);
    assert(panel_gpu(&p, "workstation#1") == &p.gpus[0] && !panel_gpu(&p, "workstation#2"));
    assert(!panel_gpu(&p, "nope") && !panel_gpu(&p, "") && !panel_gpu(&p, "workstation#0"));
    assert(panel_pool(&p, "boot-pool") == &p.pools[1] && !panel_pool(&p, "tnak") && !panel_pool(&p, ""));

    // A name longer than the field is cut on a character boundary, never mid em dash.
    char dashes[512] = "{\"planka\":{\"boards\":[{\"cards\":[{\"n\":\"";
    for (int i = 0; i < 40; i++) {
        strcat(dashes, "\xE2\x80\x94");
    }
    strcat(dashes, "\"}]}]}}");
    assert(panel_parse(dashes, strlen(dashes), &p));
    const char *cut = p.boards[0].cards[0].name;
    assert(strlen(cut) == 57 && valid_utf8(cut)); // 19 whole dashes fit in 59 bytes

    assert(!panel_parse("not json", 8, &p));
    assert(!panel_parse("[1,2]", 5, &p));
    assert(panel_parse("{}", 2, &p) && p.ngpus == 0 && p.npools == 0); // sections may be missing

    // Caps and string cuts hold against a hostile or future-grown document.
    char big[8192] = "{\"homelab\":{\"gpu\":[";
    for (int i = 0; i < 10; i++) {
        strcat(big, i ? "," : "");
        strcat(big, "{\"host\":\"a-very-long-hostname-that-does-not-fit\",\"temp\":\"hot\"}");
    }
    strcat(big, "]}}");
    assert(panel_parse(big, strlen(big), &p));
    assert(p.ngpus == PANEL_MAX_GPUS && strlen(p.gpus[0].host) == sizeof p.gpus[0].host - 1 && p.gpus[0].temp == 0);
    // A src longer than the 15-byte host field still finds it, "#n" included (the layout allows 27 bytes).
    assert(panel_gpu(&p, "a-very-long-hostname-that-d") == &p.gpus[0]);
    assert(panel_gpu(&p, "a-very-long-hostname#3") == &p.gpus[2] && !panel_gpu(&p, "a-very-long-hostname#5"));
    // ...cut on a character boundary, as panel_parse cuts it: 10 two-byte letters -> 7 fit in 15 bytes.
    const char *e10 = "\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9\xC3\xA9";
    char accents[256];
    snprintf(accents, sizeof accents, "{\"homelab\":{\"gpu\":[{\"host\":\"%s\"},{\"host\":\"%s\"}]}}", e10, e10);
    assert(panel_parse(accents, strlen(accents), &p) && strlen(p.gpus[0].host) == 14);
    char src2[32];
    snprintf(src2, sizeof src2, "%s#2", e10);
    assert(panel_gpu(&p, e10) == &p.gpus[0] && panel_gpu(&p, src2) == &p.gpus[1]);

    // 7 pools kept, as many as the aggregator's default layout makes cards for (maxDefaultPools); the 8th goes.
    char pools[512] = "{\"homelab\":{\"nas\":{\"pools\":[";
    for (int i = 1; i <= 8; i++) {
        char one[24];
        snprintf(one, sizeof one, "%s{\"n\":\"p%d\"}", i > 1 ? "," : "", i);
        strcat(pools, one);
    }
    strcat(pools, "]}}}");
    assert(panel_parse(pools, strlen(pools), &p) && p.npools == 7 && panel_pool(&p, "p7") && !panel_pool(&p, "p8"));

    if (argc > 1) {
        FILE *f = fopen(argv[1], "rb");
        assert(f);
        static char buf[16384];
        size_t n = fread(buf, 1, sizeof buf, f);
        fclose(f);
        assert(panel_parse(buf, n, &p));
        printf("%s: %zu bytes, %d gpus, %d pools, %d alerts (+%d info), %d peers, homelab stale=%d\n", argv[1], n,
               p.ngpus, p.npools, p.nalerts, p.info_alerts, p.npeers, p.homelab_stale);
        for (int i = 0; i < p.ngpus; i++) {
            printf("  gpu %s %s %s %d C %d%% %d/%d MiB %d W\n", p.gpus[i].host, p.gpus[i].name, p.gpus[i].state,
                   p.gpus[i].temp, p.gpus[i].util, p.gpus[i].vram, p.gpus[i].vram_total, p.gpus[i].power);
        }
        for (int i = 0; i < p.npools; i++) {
            printf("  pool %s %s %d/%d GiB\n", p.pools[i].name, p.pools[i].health, p.pools[i].used_gib,
                   p.pools[i].used_gib + p.pools[i].avail_gib);
        }
    }
    puts("panel_test: ok");
    return 0;
}
