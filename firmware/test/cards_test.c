// Host-side check of cards_parse(), cards_find() and cards_point(): `sh test/run.sh`.
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "cards.h"

static const char doc[] =
    "{\"cards\":{\"cpu\":{\"st\":\"warn\",\"v\":\"87 %\",\"g\":[\"AAAA\"],\"th\":50},"
    "\"ram\":{\"v\":\"12 %\",\"f\":0.121,\"sub\":\"9.2 / 76 GB\"},"
    "\"net\":{\"v\":\"0.03 Mb/s\",\"v2\":\"0.04 Mb/s\",\"g\":[\"AB\",\"CD\"]},"
    "\"gpu-workstation\":{\"g\":[\"_g\"]}},\"future\":1}";

int main(void)
{
    static cards_t cs;
    assert(cards_parse(doc, strlen(doc), &cs) && cs.n == 4);
    const card_val_t *c = cards_find(&cs, "cpu");
    assert(c && c == &cs.c[0] && c->st == CV_WARN && !strcmp(c->v, "87 %") && c->ng == 1 && c->th == 50);
    assert(!strcmp(c->g[0], "AAAA") && c->f < 0 && !c->v2[0] && !c->sub[0]);
    c = cards_find(&cs, "ram");
    assert(c && c->st == CV_OK && c->f > 0.1209f && c->f < 0.1211f && !strcmp(c->sub, "9.2 / 76 GB"));
    assert(c->th == -1 && c->ng == 0);
    c = cards_find(&cs, "net");
    assert(c && c->ng == 2 && !strcmp(c->v2, "0.04 Mb/s") && !strcmp(c->g[1], "CD"));
    assert(cards_find(&cs, "gpu-workstation")->ng == 1 && !cards_find(&cs, "nope"));

    // st: bad, stale, none, unknown, wrong type
    static const char st[] =
        "{\"cards\":{\"a\":{\"st\":\"bad\"},\"b\":{\"st\":\"stale\"},\"c\":{\"st\":\"none\"},\"d\":{\"st\":\"odd\"},"
        "\"e\":{\"st\":3},\"f\":5}}";
    assert(cards_parse(st, strlen(st), &cs) && cs.n == 5);
    assert(cs.c[0].st == CV_BAD && cs.c[1].st == CV_STALE && cs.c[2].st == CV_NONE);
    assert(cs.c[3].st == CV_OK && cs.c[4].st == CV_OK); // the non-object entry "f" is skipped

    // cuts: v is 16 bytes (15 usable), g 61 (60 usable)
    char big[512], g[80];
    memset(g, 'x', 70), g[70] = 0;
    snprintf(big, sizeof big, "{\"cards\":{\"a\":{\"v\":\"01234567890123456789\",\"g\":[\"%s\"]}}}", g);
    assert(cards_parse(big, strlen(big), &cs) && strlen(cs.c[0].v) == 15 && strlen(cs.c[0].g[0]) == 60);

    // more than CARDS_MAX entries: the rest dropped
    char *many = malloc(CARDS_MAX * 20 + 100), *p = many;
    p += sprintf(p, "{\"cards\":{");
    for (int i = 0; i < CARDS_MAX + 5; i++) {
        p += sprintf(p, "%s\"c%d\":{\"v\":\"1\"}", i ? "," : "", i);
    }
    p += sprintf(p, "}}");
    assert(cards_parse(many, strlen(many), &cs) && cs.n == CARDS_MAX);
    assert(!strcmp(cs.c[CARDS_MAX - 1].id, "c143") && !cards_find(&cs, "c144"));
    free(many);

    // characters outside the alphabet are kept; cards_point says -1
    static const char odd[] = "{\"cards\":{\"a\":{\"g\":[\"A!z\"]}}}";
    assert(cards_parse(odd, strlen(odd), &cs) && !strcmp(cs.c[0].g[0], "A!z"));
    assert(cards_point('!') == -1 && cards_point(' ') == -1 && cards_point('+') == -1 && cards_point(0) == -1);
    assert(cards_point('A') == 0 && cards_point('Z') == 25 && cards_point('a') == 26 && cards_point('g') == 32);
    assert(cards_point('0') == 52 && cards_point('9') == 61 && cards_point('-') == 62 && cards_point('_') == 63);

    // not a cards document
    assert(!cards_parse("nope", 4, &cs) && cs.n == 0);
    assert(!cards_parse("[1]", 3, &cs) && !cards_parse("{}", 2, &cs) && !cards_parse("{\"cards\":[]}", 12, &cs));
    assert(!cards_parse("{\"cards\":5}", 11, &cs));
    assert(cards_parse("{\"cards\":{}}", 12, &cs) && cs.n == 0);

    puts("cards_test: ok");
    return 0;
}
