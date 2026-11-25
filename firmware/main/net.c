// Network side of the panel. Runs on core 0 and never touches LVGL: the UI reads net_status() and
// takes panel, weather and frame copies from its own timer, so a slow or dead network can't stall the screen.
#include "net.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/stat.h>
#include <unistd.h>
#include <sys/time.h>
#include <time.h>

#include "board.h"
#include "bootlog.h"
#include "cJSON.h"
#include "esp_attr.h"
#include "esp_event.h"
#include "esp_heap_caps.h"
#include "esp_http_client.h"
#include "esp_log.h"
#include "esp_netif.h"
#include "esp_netif_sntp.h"
#include "esp_system.h"
#include "esp_task_wdt.h"
#include "esp_timer.h"
#include "esp_wifi.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/semphr.h"
#include "freertos/task.h"
#include "miniz.h" // the inflater in the ESP32-S3's ROM
#include "nvs_flash.h"
#ifdef DESKPANEL_GENERIC
#include "secrets.example.h" // the flasher page's firmware (release.sh): settings.txt sets everything
#else
#include "secrets.h" // copy secrets.example.h
#endif
#include "settings.h"

#define POLL_MS     5000
#define POLL_MAX_MS 30000
#define WX_POLL_MS  15000
#define LX_POLL_MS  15000

// Radar frames: zlib RGB565 800x368, inflated as they stream in, straight into a frame buffer.
#define FRAME_BYTES (800 * 368 * 2)
#define FRAME_BUFS  6             // 5 on screen + 1 downloading
#define NET_STACK   6144          // bytes; see the "never used" log: if its high-water mark shows under 1024 B unused after 30 min, raise this to 8192
#define PSRAM_SPARE (1024 * 1024) // free-PSRAM target; short of it, make do with one buffer fewer

// Last good document on the SD card, shown greyed after a reboot until live data arrives.
#define SD_MOUNT      "/sdcard"
#define SD_COPY       SD_MOUNT "/panel.jsn"   // 8.3 name: long file names are off in this build
#define SD_COPY_TMP   SD_MOUNT "/panel.tmp"   // written whole, then renamed over SD_COPY
#define SD_LAYOUT     SD_MOUNT "/layout.jsn"  // the layout document text, saved only when it changes
#define SD_LAYOUT_TMP SD_MOUNT "/layout.tmp"  // written whole, then renamed over SD_LAYOUT
#define SD_CARDS      SD_MOUNT "/cards.jsn"   // the cards document text, at most once a minute
#define SD_CARDS_TMP  SD_MOUNT "/cards.tmp"   // written whole, then renamed over SD_CARDS
#define SD_ASTRO      SD_MOUNT "/astro.jsn"   // the astro document text, at most once a minute
#define SD_ASTRO_TMP  SD_MOUNT "/astro.tmp"   // written whole, then renamed over SD_ASTRO
#define SD_SKY        SD_MOUNT "/sky.raw"     // the sky picture, raw RGB565 (SKY_BYTES), written when a new one completes
#define SD_SKY_TMP    SD_MOUNT "/sky.tmp"     // written whole, then renamed over SD_SKY
#define SD_BOOTLOG    SD_MOUNT "/boot.log"    // one line per boot, <= BOOTLOG_MAX, the oldest dropped
#define SD_BOOTLOG_TMP SD_MOUNT "/boot.tmp"   // written whole, then renamed over SD_BOOTLOG
#define SD_SETTINGS   SD_MOUNT "/settings.txt" // yours: read once at boot, never written
#define SAVE_EVERY_US (60LL * 1000 * 1000)    // the doc changes every poll; spare the card
#define SKY_SAVE_US   (6LL * 3600 * 1000 * 1000) // sky.raw is 285 KB: rewrite at most every 6 h (only a placeholder after a reboot)
#define SKY_DL_US     (15LL * 1000 * 1000)      // wall-clock limit for one whole sky download
#define AX_POLL_MS    60000                  // astro poll interval
#define AX_NOLOC_MS   (10 * 60000)            // ... when the aggregator has no location (404)

static const char *TAG = "net";
static portMUX_TYPE lock = portMUX_INITIALIZER_UNLOCKED; // guards st, fetched_us and wx_status
static net_status_t st;
static int64_t fetched_us = -1;
static esp_timer_handle_t retry_timer;
static int backoff_s = 1;

// The six settings: secrets.h's values, then settings.txt's lines over them (settings_load). Written once in
// net_start before Wi-Fi and the net task start; read-only after.
static settings_t settings = {.wifi_ssid = WIFI_SSID, .wifi_password = WIFI_PASSWORD, .aggregator_url = AGGREGATOR_URL,
                              .panel_token = PANEL_TOKEN, .tz = DESKPANEL_TZ, .ntp_server = NTP_SERVER};
#define FITS(m, f) _Static_assert(sizeof m <= sizeof settings.f, #m " in secrets.h is too long for settings_t")
FITS(WIFI_SSID, wifi_ssid); FITS(WIFI_PASSWORD, wifi_password); FITS(AGGREGATOR_URL, aggregator_url);
FITS(PANEL_TOKEN, panel_token); FITS(DESKPANEL_TZ, tz); FITS(NTP_SERVER, ntp_server);
static char auth[8 + sizeof settings.panel_token]; // "Bearer " + the token, built in net_start after settings_load

// Panel handoff: the net task parses into `parsed`, then copies it to `shared` under the mutex.
// Both live in PSRAM so internal RAM stays free for Wi-Fi and the bounce buffers.
EXT_RAM_BSS_ATTR static panel_t parsed, shared;
static SemaphoreHandle_t panel_mutex;
static uint32_t panel_seq;

// POSTs the UI asks for: UI -> net task; results of card dones: net task -> UI.
typedef struct {
    char kind;   // 'd' = mark card done, 'r' = notes read up to id, 'a' = fetch weather alert id
    char id[24];
} req_t;
static QueueHandle_t req_q, result_q;

// HTTP response capture (net task only): on_http appends to buf, keeping len below cap.
typedef struct {
    char *buf;
    size_t cap, len;
    bool overflow;
    char etag[32]; // the response's ETag
} body_t;

EXT_RAM_BSS_ATTR static char body[12 * 1024];
static body_t pb = {.buf = body, .cap = sizeof body};
static char etag[32];

// Weather handoff, as for the panel: parsed into wx_parsed, copied to wx_shared under wx_mutex, which
// also guards the frame set and the alert slot. The UI only try-locks it. The net task, the only
// writer, reads wx_shared without it.
EXT_RAM_BSS_ATTR static weather_t wx_parsed, wx_shared;
EXT_RAM_BSS_ATTR static char wx_body[6 * 1024], alert_body[4 * 1024];
static body_t wb = {.buf = wx_body, .cap = sizeof wx_body};
static char wx_etag[32];
static SemaphoreHandle_t wx_mutex;
static uint32_t wx_seq;
static int wx_status;

// Layout handoff: like the panel's, under panel_mutex (both are brief copies the UI only try-locks).
// The SD copy holds the document text; lx_body keeps it until the next poll replaces it.
EXT_RAM_BSS_ATTR static layout_t lx_parsed, lx_shared;
EXT_RAM_BSS_ATTR static char lx_body[12 * 1024];
static body_t lb = {.buf = lx_body, .cap = sizeof lx_body};
static char lx_etag[32];
static uint32_t lx_seq;
static int lx_status;
static bool lx_have; // lx_shared holds a layout (from the SD copy or a poll)
static bool lx_dirty = true; // layout.jsn is missing, unreadable or failed to write: rewrite on the next good poll

// Cards handoff, as the layout's (panel_mutex). cx_parsed is the net task's, cx_shared the UI's copy; cx_body
// keeps the newest 200 body (cx_len bytes) until the next poll, for the SD copy.
EXT_RAM_BSS_ATTR static cards_t cx_parsed, cx_shared;
EXT_RAM_BSS_ATTR static char cx_body[20 * 1024];
static body_t cb = {.buf = cx_body, .cap = sizeof cx_body};
static char cx_etag[32];
static size_t cx_len;
static uint32_t cx_seq;
static int cx_status;
static int64_t cx_fetched_us = -1, cx_saved_us = -1;
static bool cx_dirty; // cx_body holds a document not yet on the card

// Astro handoff, as the cards': panel_mutex guards ax_shared/ax_seq; ax_body keeps the newest 200 body for the SD copy.
EXT_RAM_BSS_ATTR static astro_t ax_parsed, ax_shared;
EXT_RAM_BSS_ATTR static char ax_body[2 * 1024];
static body_t ab = {.buf = ax_body, .cap = sizeof ax_body};
static char ax_etag[32];
static size_t ax_len;
static uint32_t ax_seq;
static int ax_status;
static int64_t ax_saved_us = -1, ax_fetched_us = -1;
static int64_t sky_saved_us = -1; // sky.raw is rewritten at most every SKY_SAVE_US; -1 = not since boot
static bool ax_dirty; // ax_body holds a document not yet on the card

// Sky pictures (A5): two PSRAM buffers, or one if the second wouldn't fit. sky_front is the complete picture the UI
// may draw; a download goes into the other buffer and swaps in only when complete. These are under wx_mutex
// (the net task, the only writer, reads them without it). sky_ui is the buffer the UI last took.
static uint16_t *sky_buf[2];
static int nsky, sky_front = -1, sky_ui = -1;
static long long sky_t;  // valid time of the front picture, 0 = loaded from the SD card (time unknown)
static uint32_t sky_seq;

// Frame ring. A buffer is written only while it's in neither the published set nor the set the UI
// last took, so nothing the UI may be drawing ever changes under it.
static uint16_t *fbuf[FRAME_BUFS];
static int nbuf;
static long long held[FRAME_BUFS];  // valid time inflated into each buffer, 0 = none (net task only)
static net_frames_t frames_pub;     // these four under wx_mutex
static uint32_t frames_seq;
static unsigned pub_mask, ui_mask;  // buffers in frames_pub / in the set the UI last took
EXT_RAM_BSS_ATTR static tinfl_decompressor inflater; // ~11 KB: too big for the task stack

// Alert slot: alert_body is the net task's while !alert_ready, then net_alert_take's (both under wx_mutex).
static bool alert_ready;
static int alert_n, alert_status;

static bool sd_ok;
static int64_t saved_us = -1; // when the copy was last written (esp_timer)
static time_t copy_saved_at;  // wall time of the copy shown since boot; 0 = none

net_status_t net_status(void)
{
    taskENTER_CRITICAL(&lock);
    net_status_t s = st;
    taskEXIT_CRITICAL(&lock);
    return s;
}

bool net_wifi_unset(void) { return !settings.wifi_ssid[0]; }

static void set_state(net_state_t s)
{
    taskENTER_CRITICAL(&lock);
    st.state = s;
    taskEXIT_CRITICAL(&lock);
}

bool net_panel_take(panel_t *out, uint32_t *seen)
{
    if (!panel_mutex || xSemaphoreTake(panel_mutex, 0) != pdTRUE) {
        return false;
    }
    bool fresh = panel_seq != *seen;
    if (fresh) {
        *out = shared;
        *seen = panel_seq;
    }
    xSemaphoreGive(panel_mutex);
    return fresh;
}

bool net_weather_take(weather_t *out, uint32_t *seen)
{
    if (!wx_mutex || xSemaphoreTake(wx_mutex, 0) != pdTRUE) {
        return false;
    }
    bool fresh = wx_seq != *seen;
    if (fresh) {
        *out = wx_shared;
        *seen = wx_seq;
    }
    xSemaphoreGive(wx_mutex);
    return fresh;
}

bool net_layout_take(layout_t *out, uint32_t *seen)
{
    if (!panel_mutex || xSemaphoreTake(panel_mutex, 0) != pdTRUE) {
        return false;
    }
    bool fresh = lx_seq != *seen;
    if (fresh) {
        *out = lx_shared;
        *seen = lx_seq;
    }
    xSemaphoreGive(panel_mutex);
    return fresh;
}

bool net_cards_take(cards_t *out, uint32_t *seen)
{
    if (!panel_mutex || xSemaphoreTake(panel_mutex, 0) != pdTRUE) {
        return false;
    }
    bool fresh = cx_seq != *seen;
    if (fresh) {
        *out = cx_shared;
        *seen = cx_seq;
    }
    xSemaphoreGive(panel_mutex);
    return fresh;
}

int net_cards_status(void)
{
    taskENTER_CRITICAL(&lock);
    int s = cx_status;
    taskEXIT_CRITICAL(&lock);
    return s;
}

int64_t net_cards_age_ms(void)
{
    taskENTER_CRITICAL(&lock);
    int64_t t = cx_fetched_us;
    taskEXIT_CRITICAL(&lock);
    return t >= 0 ? (esp_timer_get_time() - t) / 1000 : -1;
}

bool net_astro_take(astro_t *out, uint32_t *seen)
{
    if (!panel_mutex || xSemaphoreTake(panel_mutex, 0) != pdTRUE) {
        return false;
    }
    bool fresh = ax_seq != *seen;
    if (fresh) {
        *out = ax_shared;
        *seen = ax_seq;
    }
    xSemaphoreGive(panel_mutex);
    return fresh;
}

int64_t net_astro_age_ms(void)
{
    taskENTER_CRITICAL(&lock);
    int64_t t = ax_fetched_us;
    taskEXIT_CRITICAL(&lock);
    return t >= 0 ? (esp_timer_get_time() - t) / 1000 : -1;
}

int net_astro_status(void)
{
    taskENTER_CRITICAL(&lock);
    int s = ax_status;
    taskEXIT_CRITICAL(&lock);
    return s;
}

bool net_sky_take(const uint16_t **px, long long *t, uint32_t *seen)
{
    if (!wx_mutex || xSemaphoreTake(wx_mutex, 0) != pdTRUE) {
        return false;
    }
    bool fresh = sky_front >= 0 && sky_seq != *seen;
    if (fresh) {
        *px = sky_buf[sky_front];
        *t = sky_t;
        *seen = sky_seq;
        sky_ui = sky_front; // from now on the UI draws this one, and may stop drawing the last
    }
    xSemaphoreGive(wx_mutex);
    return fresh;
}

int net_layout_status(void)
{
    taskENTER_CRITICAL(&lock);
    int s = lx_status;
    taskEXIT_CRITICAL(&lock);
    return s;
}

int net_weather_status(void)
{
    taskENTER_CRITICAL(&lock);
    int s = wx_status;
    taskEXIT_CRITICAL(&lock);
    return s;
}

bool net_frames_take(net_frames_t *out, uint32_t *seen)
{
    if (!wx_mutex || xSemaphoreTake(wx_mutex, 0) != pdTRUE) {
        return false;
    }
    bool fresh = frames_seq != *seen;
    if (fresh) {
        *out = frames_pub;
        *seen = frames_seq;
        ui_mask = pub_mask; // from now on the UI draws these, and may stop drawing the last set
    }
    xSemaphoreGive(wx_mutex);
    return fresh;
}

bool net_alert_take(int *n, int *status, char *buf, size_t cap)
{
    if (!wx_mutex || xSemaphoreTake(wx_mutex, 0) != pdTRUE) {
        return false;
    }
    bool ready = alert_ready;
    if (ready) {
        *n = alert_n;
        *status = alert_status;
        strlcpy(buf, alert_body, cap);
        alert_ready = false;
    }
    xSemaphoreGive(wx_mutex);
    return ready;
}

int64_t net_panel_age_ms(void)
{
    taskENTER_CRITICAL(&lock);
    int64_t t = fetched_us;
    taskEXIT_CRITICAL(&lock);
    if (t >= 0) {
        return (esp_timer_get_time() - t) / 1000;
    }
    return copy_saved_at ? (int64_t)(time(NULL) - copy_saved_at) * 1000 : -1; // the saved copy's age
}

// Write buf whole to tmp, then rename it over path, so a power cut leaves the old copy or the new one,
// never half of one. FATFS rename can't overwrite: if it fails, remove the old file and rename again; in
// that gap tmp is complete and the boot loaders fall back to it.
static bool save_atomic(const char *path, const char *tmp, const char *buf, size_t len)
{
    FILE *f = fopen(tmp, "w");
    if (!f) {
        ESP_LOGW(TAG, "can't write %s", tmp);
        return false;
    }
    bool ok = fwrite(buf, 1, len, f) == len;
    ok = fclose(f) == 0 && ok;
    if (ok && rename(tmp, path) != 0) {
        unlink(path);
        ok = rename(tmp, path) == 0;
    }
    if (!ok) {
        ESP_LOGW(TAG, "can't save %s", path);
    }
    return ok;
}

static void settings_warn(const char *msg) { ESP_LOGW(TAG, "%s", msg); }

// Once, right after the SD mount, before anything reads settings. Logs which keys came from the file, never a value.
static void settings_load(void)
{
    FILE *f = sd_ok ? fopen(SD_SETTINGS, "r") : NULL;
    if (f) {
        char *txt = heap_caps_malloc(SETTINGS_FILE_MAX + 1, MALLOC_CAP_SPIRAM); // once, before the frame buffers
        size_t n = txt ? fread(txt, 1, SETTINGS_FILE_MAX + 1, f) : 0;
        bool bad = ferror(f); // an SD error mid-read: never apply a cut-off file (half a password)
        fclose(f);
        if (!txt) {
            ESP_LOGW(TAG, "settings: no memory to read settings.txt: built in used");
        } else if (bad) {
            ESP_LOGW(TAG, "settings: can't read settings.txt: built in used");
        } else if (n > SETTINGS_FILE_MAX) {
            ESP_LOGW(TAG, "settings: settings.txt is over %d bytes: ignored, built in used", SETTINGS_FILE_MAX);
        } else {
            unsigned got = settings_parse(&settings, txt, n, settings_warn);
            char keys[96] = "";
            for (int i = 0; i < SETTINGS_KEYS; i++) {
                if (got & 1u << i) {
                    strlcat(keys, " ", sizeof keys);
                    strlcat(keys, settings_key[i], sizeof keys);
                }
            }
            ESP_LOGI(TAG, "settings: from settings.txt:%s", got ? keys : " nothing");
        }
        heap_caps_free(txt);
    } else {
        ESP_LOGI(TAG, "settings: built in%s", sd_ok ? " (no settings.txt)" : "");
    }
    snprintf(auth, sizeof auth, "Bearer %s", settings.panel_token);
}

// What the next boot reports about this run. Survives panics, watchdogs and software resets, not power loss
// (hence the magic, and never trusted after a power-on).
static RTC_NOINIT_ATTR struct { uint32_t magic, boot, up_s; int64_t wall; } run_rec;
#define RUN_MAGIC 0x64706231
static char boot_hdr[48]; // "12 task_wdt 273600": boot, reason, the previous run's uptime in s (0: unknown)

static void run_tick(void *arg)
{
    run_rec.up_s = (uint32_t)(esp_timer_get_time() / 1000000);
    if (st.time_ok) {
        run_rec.wall = time(NULL);
    }
}

// Once, in net_start: report why we booted and how the last run ended (serial, /boot.log, X-Panel-Boot),
// then start the minute timer that keeps run_rec current for the next boot.
static void boot_record(void)
{
    esp_reset_reason_t reason = esp_reset_reason();
    const char *tok, *words;
    switch (reason) {
    case ESP_RST_POWERON:    tok = "poweron";    words = "power-on"; break;
    case ESP_RST_EXT:        tok = "ext";        words = "reset pin"; break;
    case ESP_RST_SW:         tok = "sw";         words = "software restart"; break;
    case ESP_RST_PANIC:      tok = "panic";      words = "crash (panic)"; break;
    case ESP_RST_INT_WDT:    tok = "int_wdt";    words = "crash (interrupt watchdog)"; break;
    case ESP_RST_TASK_WDT:   tok = "task_wdt";   words = "crash (task watchdog)"; break;
    case ESP_RST_WDT:        tok = "wdt";        words = "crash (watchdog)"; break;
    case ESP_RST_DEEPSLEEP:  tok = "deepsleep";  words = "deep sleep"; break;
    case ESP_RST_BROWNOUT:   tok = "brownout";   words = "brownout (power dip)"; break;
    case ESP_RST_SDIO:       tok = "sdio";       words = "SDIO reset"; break;
    case ESP_RST_USB:        tok = "usb";        words = "USB reset (flash or monitor)"; break;
    case ESP_RST_JTAG:       tok = "jtag";       words = "JTAG reset"; break;
    case ESP_RST_EFUSE:      tok = "efuse";      words = "eFuse error"; break;
    case ESP_RST_PWR_GLITCH: tok = "pwr_glitch"; words = "power glitch"; break;
    case ESP_RST_CPU_LOCKUP: tok = "cpu_lockup"; words = "crash (CPU lockup)"; break;
    default:                 tok = "unknown";    words = "unknown reset"; break;
    }
    bool kept = run_rec.magic == RUN_MAGIC && reason != ESP_RST_POWERON;
    uint32_t boot = kept ? run_rec.boot + 1 : 1, prev_up = kept ? run_rec.up_s : 0;
    int64_t prev_wall = kept ? run_rec.wall : 0;
    run_rec.magic = RUN_MAGIC;
    run_rec.boot = boot;
    run_rec.up_s = 0;
    run_rec.wall = st.time_ok ? time(NULL) : 0;

    char line[200], ts[20], dur[24], *p = line, *end = line + sizeof line;
    time_t now = time(NULL);
    if (st.time_ok) {
        strftime(ts, sizeof ts, "%Y-%m-%d %H:%M", localtime(&now));
    } else {
        strlcpy(ts, "time unknown", sizeof ts);
    }
    p += snprintf(p, end - p, "%s  boot %u: %s", ts, (unsigned)boot, words);
    if (kept) {
        bootlog_dur(dur, sizeof dur, prev_up);
        p += snprintf(p, end - p, "; previous run up %s", dur);
        if (prev_wall > 1700000000 && prev_wall < 4102444800) { // odd RTC contents must never crash the boot
            time_t w = (time_t)prev_wall;
            struct tm *lt = localtime(&w);
            if (lt && strftime(ts, sizeof ts, "%Y-%m-%d %H:%M", lt)) {
                p += snprintf(p, end - p, ", last seen %s", ts);
            } else {
                p += snprintf(p, end - p, ", last seen unknown");
            }
        }
    }
    snprintf(p, end - p, "\n");
    ESP_LOGI(TAG, "%s", line);
    snprintf(boot_hdr, sizeof boot_hdr, "%u %s %u", (unsigned)boot, tok, (unsigned)prev_up);

    // Runs once, before the radar frame buffers exist; a failed malloc just skips the SD log.
    char *bootlog_buf = sd_ok ? heap_caps_malloc(BOOTLOG_MAX, MALLOC_CAP_SPIRAM) : NULL;
    if (bootlog_buf) {
        FILE *f = fopen(SD_BOOTLOG, "r");
        if (!f) {
            f = fopen(SD_BOOTLOG_TMP, "r"); // a power cut between save_atomic's remove and rename
        }
        size_t len = 0;
        if (f) {
            len = fread(bootlog_buf, 1, BOOTLOG_MAX, f);
            fclose(f);
        }
        len = bootlog_add(bootlog_buf, len, line);
        save_atomic(SD_BOOTLOG, SD_BOOTLOG_TMP, bootlog_buf, len);
        heap_caps_free(bootlog_buf);
    }

    static esp_timer_handle_t tick;
    const esp_timer_create_args_t ta = {.callback = run_tick, .name = "run_rec"};
    if (esp_timer_create(&ta, &tick) == ESP_OK) {
        esp_timer_start_periodic(tick, 60LL * 1000 * 1000);
    }
}

static void save_copy(void)
{
    if (!sd_ok || (saved_us >= 0 && esp_timer_get_time() - saved_us < SAVE_EVERY_US)) {
        return;
    }
    save_atomic(SD_COPY, SD_COPY_TMP, body, pb.len);
    saved_us = esp_timer_get_time(); // a failed save waits its minute too: spare a failing card
}

// Parse the panel document saved at `path` into parsed/body; *at gets its modification time.
static bool read_copy(const char *path, time_t *at)
{
    struct stat st;
    FILE *f = stat(path, &st) == 0 ? fopen(path, "r") : NULL;
    if (!f) {
        return false;
    }
    pb.len = fread(body, 1, sizeof body, f);
    fclose(f);
    *at = st.st_mtime;
    return panel_parse(body, pb.len, &parsed);
}

// At boot, before the UI exists: publish the saved copy so the screen starts with the last data
// (greyed, "STALE <age>") instead of "NO DATA". Its notes count as read: history, not news.
// panel.tmp is the fallback for a power cut between save_atomic's remove and rename.
static void load_copy(void)
{
    time_t at;
    if (!read_copy(SD_COPY, &at) && !read_copy(SD_COPY_TMP, &at)) {
        return;
    }
    for (int i = 0; i < parsed.nnotes; i++) {
        parsed.notes[i].read = true;
    }
    shared = parsed;
    panel_seq++;
    copy_saved_at = at;
    ESP_LOGI(TAG, "showing the copy saved %lld s ago", (long long)(time(NULL) - copy_saved_at));
}

// Parse the layout document saved at `path` into lx_parsed/lx_body.
static bool read_layout(const char *path)
{
    FILE *f = fopen(path, "r");
    if (!f) {
        return false;
    }
    lb.len = fread(lx_body, 1, sizeof lx_body, f);
    fclose(f);
    return layout_parse(lx_body, lb.len, &lx_parsed);
}

// Boot: publish the saved layout document so the tabs exist offline. layout.tmp is the fallback for a
// power cut between save_atomic's remove and rename; either way the next good poll rewrites layout.jsn.
static void load_layout_copy(void)
{
    if (read_layout(SD_LAYOUT)) {
        lx_dirty = false;
    } else if (!read_layout(SD_LAYOUT_TMP)) {
        return;
    }
    lx_shared = lx_parsed;
    lx_have = true;
    lx_seq++;
    ESP_LOGI(TAG, "layout: showing the saved copy, %d tabs", lx_shared.ntabs);
}

// Parse the cards document saved at `path` into cx_parsed/cx_body.
static bool read_cards(const char *path)
{
    FILE *f = fopen(path, "r");
    if (!f) {
        return false;
    }
    cx_len = fread(cx_body, 1, sizeof cx_body, f);
    fclose(f);
    return cards_parse(cx_body, cx_len, &cx_parsed);
}

// Boot: publish the saved cards document (the first take), greyed by the UI until a poll replaces it.
// cards.tmp is the fallback for a power cut between save_atomic's remove and rename.
static void load_cards_copy(void)
{
    if (!read_cards(SD_CARDS) && !read_cards(SD_CARDS_TMP)) {
        return;
    }
    cx_shared = cx_parsed;
    cx_seq++;
    ESP_LOGI(TAG, "cards: showing the saved copy, %d entries", cx_shared.n);
}

static bool read_astro(const char *path)
{
    FILE *f = fopen(path, "r");
    if (!f) {
        return false;
    }
    ax_len = fread(ax_body, 1, sizeof ax_body, f);
    fclose(f);
    return astro_parse(ax_body, ax_len, &ax_parsed);
}

// Boot: publish the saved astro document (the first take). astro.tmp is the fallback for a power cut between
// save_atomic's remove and rename.
static void load_astro_copy(void)
{
    if (!read_astro(SD_ASTRO) && !read_astro(SD_ASTRO_TMP)) {
        return;
    }
    ax_shared = ax_parsed;
    ax_seq++;
    ESP_LOGI(TAG, "astro: showing the saved copy");
}

// Boot: publish the saved sky picture (the first take, time 0 = unknown, so the first good poll fetches the
// current one). Only a file of exactly SKY_BYTES counts; sky.tmp is the power-cut fallback. Needs sky_buf[0]. It was
// saved fitted (astro_sky_fit()); one saved by older firmware shows unfitted until the first download.
static void load_sky_copy(void)
{
    if (!nsky) {
        return;
    }
    const char *paths[] = {SD_SKY, SD_SKY_TMP};
    for (int i = 0; i < 2; i++) {
        FILE *f = fopen(paths[i], "rb");
        if (!f) {
            continue;
        }
        bool whole = fread(sky_buf[0], 1, SKY_BYTES, f) == SKY_BYTES && fgetc(f) == EOF;
        fclose(f);
        if (whole) {
            sky_front = 0;
            sky_t = 0;
            sky_saved_us = esp_timer_get_time(); // the card has one: no rewrite for 30 min
            sky_seq++;
            ESP_LOGI(TAG, "sky: showing the saved picture");
            return;
        }
    }
}

static bool request(char kind, const char *id)
{
    req_t r = {.kind = kind};
    strlcpy(r.id, id, sizeof r.id);
    return req_q && xQueueSend(req_q, &r, 0) == pdTRUE;
}

bool net_mark_done(const char *card_id) { return request('d', card_id); }

bool net_mark_read(int upto)
{
    char id[16];
    snprintf(id, sizeof id, "%d", upto);
    return request('r', id);
}

bool net_fetch_alert(int n)
{
    char id[16];
    snprintf(id, sizeof id, "%d", n);
    return request('a', id);
}

bool net_done_result(net_done_t *out)
{
    return result_q && xQueueReceive(result_q, out, 0) == pdTRUE;
}

static bool watched; // the net task is subscribed to the task watchdog (net_task)

// Feed the task watchdog. Net task only (every caller below is), and only once subscribed: no "task not found" spam.
static void feed(void)
{
    if (watched) {
        esp_task_wdt_reset();
    }
}

// One request to settings.aggregator_url + path on its own connection; `on` (if any) gets the response events
// with `user`. Returns the HTTP status, or -1 if the aggregator couldn't be reached.
static int http(esp_http_client_method_t method, const char *path, http_event_handle_cb on, void *user)
{
    feed(); // each request starts with the full 120 s: worst ~20 s with an IP URL, 42-54 s with a host name and dead DNS
    char url[160];
    snprintf(url, sizeof url, "%s%s", settings.aggregator_url, path);
    esp_http_client_config_t cfg = {
        .url = url, .method = method, .timeout_ms = 10000, .event_handler = on, .user_data = user};
    esp_http_client_handle_t c = esp_http_client_init(&cfg);
    if (!c) {
        return -1;
    }
    esp_http_client_set_header(c, "Authorization", auth);
    int status = esp_http_client_perform(c) == ESP_OK ? esp_http_client_get_status_code(c) : -1;
    esp_http_client_cleanup(c);
    ESP_LOGI(TAG, "%s %s -> %d", method == HTTP_METHOD_POST ? "POST" : "GET", path, status);
    return status;
}

// A keep-alive client for one poll: settings.aggregator_url + path, with the token; NULL if the URL won't parse.
// esp_http_client_init copies the URL (esp_http_client_set_url keeps its own host/path strings), so url may go.
static esp_http_client_handle_t poll_client(esp_http_client_config_t cfg, const char *path, body_t *b)
{
    char url[160];
    snprintf(url, sizeof url, "%s%s", settings.aggregator_url, path);
    cfg.url = url;
    cfg.user_data = b;
    esp_http_client_handle_t c = esp_http_client_init(&cfg);
    if (c) {
        esp_http_client_set_header(c, "Authorization", auth);
    }
    return c;
}

static void retry(void *arg) { esp_wifi_connect(); }

static void on_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    if (base == WIFI_EVENT && id == WIFI_EVENT_STA_START) {
        set_state(NET_CONNECTING);
        esp_wifi_connect();
    } else if (base == WIFI_EVENT && id == WIFI_EVENT_STA_DISCONNECTED) {
        set_state(NET_CONNECTING);
        ESP_LOGW(TAG, "wifi disconnected (reason %d), retry in %d s",
                 ((wifi_event_sta_disconnected_t *)data)->reason, backoff_s);
        esp_timer_start_once(retry_timer, backoff_s * 1000000ULL);
        backoff_s = backoff_s < 32 ? backoff_s * 2 : 60;
    } else if (base == IP_EVENT && id == IP_EVENT_STA_GOT_IP) {
        ESP_LOGI(TAG, "wifi up, ip " IPSTR, IP2STR(&((ip_event_got_ip_t *)data)->ip_info.ip));
        backoff_s = 1;
        set_state(NET_UP);
    }
}

static void on_time_sync(struct timeval *tv)
{
    board_rtc_write(tv->tv_sec);
    taskENTER_CRITICAL(&lock);
    st.time_ok = true;
    taskEXIT_CRITICAL(&lock);
    ESP_LOGI(TAG, "NTP sync, RTC updated");
}

static esp_err_t on_http(esp_http_client_event_t *e)
{
    body_t *b = e->user_data;
    if (e->event_id == HTTP_EVENT_ON_HEADER && strcasecmp(e->header_key, "ETag") == 0) {
        strlcpy(b->etag, e->header_value, sizeof b->etag);
    } else if (e->event_id == HTTP_EVENT_ON_DATA) {
        if (b->len + e->data_len < b->cap) {
            memcpy(b->buf + b->len, e->data, e->data_len);
            b->len += e->data_len;
        } else {
            b->overflow = true;
        }
    }
    return ESP_OK;
}

// A GET on a keep-alive handle whose on_http captures into b, conditional on the ETag we hold.
static esp_err_t get_doc(esp_http_client_handle_t c, body_t *b, const char *held_etag)
{
    feed(); // as in http(), with 4 s timeouts: ~8 s with an IP URL
    *b = (body_t){.buf = b->buf, .cap = b->cap};
    if (held_etag[0]) {
        esp_http_client_set_header(c, "If-None-Match", held_etag);
    } else {
        esp_http_client_delete_header(c, "If-None-Match");
    }
    return esp_http_client_perform(c);
}

// One GET /api/v1/panel. 304 means "still fresh"; 200 is parsed and published.
static bool poll_panel(esp_http_client_handle_t c)
{
    esp_err_t err = get_doc(c, &pb, etag);
    int status = esp_http_client_get_status_code(c);
    if (err != ESP_OK) {
        ESP_LOGW(TAG, "panel: %s", esp_err_to_name(err));
        return false;
    }
    if (status == 304) {
        return true;
    }
    if (status != 200 || pb.overflow || !panel_parse(body, pb.len, &parsed)) {
        ESP_LOGW(TAG, "panel: HTTP %d, %u bytes%s", status, (unsigned)pb.len, pb.overflow ? ", too big" : "");
        return false;
    }
    strlcpy(etag, pb.etag, sizeof etag);
    xSemaphoreTake(panel_mutex, portMAX_DELAY); // the UI side only ever try-locks, so this is brief
    shared = parsed;
    panel_seq++;
    xSemaphoreGive(panel_mutex);
    static bool live;
    if (!live) {
        live = true;
        ESP_LOGI(TAG, "panel: first live document, %u bytes, %d gpus, %d pools, %d peers", (unsigned)pb.len,
                 parsed.ngpus, parsed.npools, parsed.npeers);
    }
    save_copy();
    return true;
}

// One GET /api/v1/weather, like poll_panel. 404 means the aggregator has no weather (not set up, or
// older than this firmware): net_weather_status says so and nothing else changes. Returns false if
// the aggregator didn't answer properly, which counts toward the shared back-off.
static bool poll_weather(esp_http_client_handle_t c)
{
    esp_err_t err = get_doc(c, &wb, wx_etag);
    int status = err == ESP_OK ? esp_http_client_get_status_code(c) : -1;
    if (err != ESP_OK) {
        ESP_LOGW(TAG, "weather: %s", esp_err_to_name(err));
    } else if (status != wx_status && status != 200 && status != 304) { // once, not every 15 s
        ESP_LOGW(TAG, "weather: HTTP %d%s", status, status == 404 ? ", not set up on the aggregator" : "");
    }
    taskENTER_CRITICAL(&lock);
    wx_status = status;
    taskEXIT_CRITICAL(&lock);
    if (status != 200) {
        return status == 304 || status == 404;
    }
    if (wb.overflow || !weather_parse(wx_body, wb.len, &wx_parsed)) {
        ESP_LOGW(TAG, "weather: %u bytes%s, unreadable", (unsigned)wb.len, wb.overflow ? ", too big" : "");
        return false;
    }
    strlcpy(wx_etag, wb.etag, sizeof wx_etag);
    xSemaphoreTake(wx_mutex, portMAX_DELAY); // the UI side only ever try-locks, so this is brief
    wx_shared = wx_parsed;
    wx_seq++;
    xSemaphoreGive(wx_mutex);
    static bool live;
    if (!live) {
        live = true;
        ESP_LOGI(TAG, "weather: first document, %d frames, %d alerts", wx_shared.nframes, wx_shared.nalerts);
    }
    return true;
}

// One GET /api/v1/layout, like poll_weather: 404 = aggregator too old (status says so, no back-off);
// 5xx = it just restarted and has no layout yet (503): keep what's shown, no back-off, no SD write.
// A changed document is published and written to the SD card; an unchanged one touches neither.
static bool poll_layout(esp_http_client_handle_t c)
{
    // No If-None-Match while the SD copy needs rewriting: a 304 would skip the save.
    esp_err_t err = get_doc(c, &lb, lx_dirty && sd_ok ? "" : lx_etag);
    int status = err == ESP_OK ? esp_http_client_get_status_code(c) : -1;
    if (status != lx_status && status != 200 && status != 304) { // once, not every 15 s
        if (err != ESP_OK) {
            ESP_LOGW(TAG, "layout: %s", esp_err_to_name(err));
        } else {
            ESP_LOGW(TAG, "layout: HTTP %d%s", status,
                     status == 404 ? ", aggregator too old" : status >= 500 ? ", keeping the layout shown" : "");
        }
    }
    taskENTER_CRITICAL(&lock);
    lx_status = status;
    taskEXIT_CRITICAL(&lock);
    if (status != 200) {
        return status == 304 || status == 404 || status >= 500;
    }
    if (lb.overflow || !layout_parse(lx_body, lb.len, &lx_parsed)) {
        ESP_LOGW(TAG, "layout: %u bytes%s, unreadable", (unsigned)lb.len, lb.overflow ? ", too big" : "");
        return false;
    }
    strlcpy(lx_etag, lb.etag, sizeof lx_etag);
    if (!lx_have || !layout_equal(&lx_parsed, &lx_shared)) {
        xSemaphoreTake(panel_mutex, portMAX_DELAY); // the UI side only ever try-locks, so this is brief
        lx_shared = lx_parsed;
        lx_seq++;
        xSemaphoreGive(panel_mutex);
        lx_have = true;
        lx_dirty = true;
        ESP_LOGI(TAG, "layout: changed, %u bytes, %d tabs", (unsigned)lb.len, lx_shared.ntabs);
    }
    if (sd_ok && lx_dirty) { // changed, or the card's copy is missing/bad: otherwise no write
        lx_dirty = !save_atomic(SD_LAYOUT, SD_LAYOUT_TMP, lx_body, lb.len);
    }
    return true;
}

// Write the newest cards body to the card if it's new and the last write was a minute ago. A failed write
// waits its minute too (spare a failing card) and isn't retried until the body changes again.
static void save_cards(void)
{
    if (!sd_ok || !cx_dirty || (cx_saved_us >= 0 && esp_timer_get_time() - cx_saved_us < SAVE_EVERY_US)) {
        return;
    }
    save_atomic(SD_CARDS, SD_CARDS_TMP, cx_body, cx_len);
    cx_saved_us = esp_timer_get_time();
    cx_dirty = false;
}

// One GET /api/v1/cards, like poll_layout: 404 = aggregator too old (the caller polls every 60 s then);
// 5xx = not ready: keep what's shown, no back-off, no SD write. Anything but a clean 200 or 304 may have
// overwritten cx_body (an error page, or a body cut off mid-transfer over the old one), so a save still
// pending is dropped (the next 200 re-arms it).
static bool poll_cards(esp_http_client_handle_t c)
{
    esp_err_t err = get_doc(c, &cb, cx_etag);
    int status = err == ESP_OK ? esp_http_client_get_status_code(c) : -1;
    if (status != cx_status && status != 200 && status != 304) { // once, not every poll
        if (err != ESP_OK) {
            ESP_LOGW(TAG, "cards: %s", esp_err_to_name(err));
        } else {
            ESP_LOGW(TAG, "cards: HTTP %d%s", status,
                     status == 404 ? ", aggregator too old" : status >= 500 ? ", keeping the cards shown" : "");
        }
    }
    taskENTER_CRITICAL(&lock);
    cx_status = status;
    if (status == 200 || status == 304) {
        cx_fetched_us = esp_timer_get_time();
    }
    taskEXIT_CRITICAL(&lock);
    if (status != 200 && (status != 304 || cb.len)) { // a 304 has no body: if one came, it overwrote cx_body
        cx_dirty = false;
        cx_etag[0] = 0; // the next poll must be a full 200 that re-arms the save; a 304 would leave the SD copy missing
    }
    if (status == 304) {
        save_cards(); // a document held back by the once-a-minute limit
        return true;
    }
    if (status != 200) {
        return status == 404 || status >= 500;
    }
    cx_dirty = false;
    if (cb.overflow || !cards_parse(cx_body, cb.len, &cx_parsed)) {
        ESP_LOGW(TAG, "cards: %u bytes%s, unreadable", (unsigned)cb.len, cb.overflow ? ", too big" : "");
        return false;
    }
    strlcpy(cx_etag, cb.etag, sizeof cx_etag);
    cx_len = cb.len;
    xSemaphoreTake(panel_mutex, portMAX_DELAY); // the UI side only ever try-locks, so this is brief
    cx_shared = cx_parsed;
    cx_seq++;
    xSemaphoreGive(panel_mutex);
    cx_dirty = true;
    save_cards();
    return true;
}

static void save_astro(void)
{
    if (!sd_ok || !ax_dirty || (ax_saved_us >= 0 && esp_timer_get_time() - ax_saved_us < SAVE_EVERY_US)) {
        return;
    }
    save_atomic(SD_ASTRO, SD_ASTRO_TMP, ax_body, ax_len);
    ax_saved_us = esp_timer_get_time();
    ax_dirty = false;
}

// One GET /api/v1/astro, like poll_cards. 404 = the aggregator has no location (the caller polls every 10 min
// then); 5xx = not ready: keep what's shown. false only when unreachable or unreadable (the caller backs off).
static bool poll_astro(esp_http_client_handle_t c)
{
    esp_err_t err = get_doc(c, &ab, ax_etag);
    int status = err == ESP_OK ? esp_http_client_get_status_code(c) : -1;
    if (status != ax_status && status != 200 && status != 304) { // once, not every poll
        if (err != ESP_OK) {
            ESP_LOGW(TAG, "astro: %s", esp_err_to_name(err));
        } else {
            ESP_LOGW(TAG, "astro: HTTP %d%s", status, status == 404 ? ", no location set" : "");
        }
    }
    taskENTER_CRITICAL(&lock);
    ax_status = status;
    if (status == 200 || status == 304) {
        ax_fetched_us = esp_timer_get_time();
    }
    taskEXIT_CRITICAL(&lock);
    if (status != 200 && (status != 304 || ab.len)) { // a 304 with a body overwrote ax_body
        ax_dirty = false;
        ax_etag[0] = 0;
    }
    if (status == 304) {
        save_astro();
        return true;
    }
    if (status != 200) {
        return status == 404 || status >= 500;
    }
    ax_dirty = false;
    if (ab.overflow || !astro_parse(ax_body, ab.len, &ax_parsed)) {
        ESP_LOGW(TAG, "astro: %u bytes%s, unreadable", (unsigned)ab.len, ab.overflow ? ", too big" : "");
        ax_etag[0] = 0;
        return false;
    }
    strlcpy(ax_etag, ab.etag, sizeof ax_etag);
    ax_len = ab.len;
    xSemaphoreTake(panel_mutex, portMAX_DELAY);
    ax_shared = ax_parsed;
    ax_seq++;
    xSemaphoreGive(panel_mutex);
    ax_dirty = true;
    save_astro();
    return true;
}

// The buffer holding frame t, or -1.
static int holding(long long t)
{
    for (int b = 0; t > 0 && b < nbuf; b++) {
        if (held[b] == t) {
            return b;
        }
    }
    return -1;
}

// A frame being inflated as it streams in (net task only).
typedef struct {
    uint8_t *out;
    size_t cap;      // bytes the picture holds (FRAME_BYTES or SKY_BYTES)
    int64_t deadline_us; // esp_timer time to give up the whole download; 0 = none
    size_t len;      // bytes inflated so far
    tinfl_status st; // NEEDS_MORE_INPUT until the stream ends or breaks
} inflate_t;

static esp_err_t on_frame(esp_http_client_event_t *e)
{
    inflate_t *f = e->user_data;
    if (e->event_id != HTTP_EVENT_ON_DATA) {
        return ESP_OK;
    }
    if (f->deadline_us && esp_timer_get_time() > f->deadline_us) {
        f->st = TINFL_STATUS_FAILED; // too slow overall: a trickling link must not hold the net task
        esp_http_client_close(e->client); // the read in perform() then fails and it returns
        return ESP_OK;
    }
    if (f->st == TINFL_STATUS_NEEDS_MORE_INPUT) {
        feed(); // a picture still arriving is no hang (frames have no deadline); a stream past its end isn't
    }
    const mz_uint8 *in = e->data;
    size_t left = e->data_len;
    while (left && f->st == TINFL_STATUS_NEEDS_MORE_INPUT) { // tinfl may stop before the chunk's end
        size_t n_in = left, n_out = f->cap - f->len;
        f->st = tinfl_decompress(&inflater, in, &n_in, f->out, f->out + f->len, &n_out,
                                 TINFL_FLAG_PARSE_ZLIB_HEADER | TINFL_FLAG_HAS_MORE_INPUT |
                                     TINFL_FLAG_USING_NON_WRAPPING_OUTPUT_BUF);
        if (!n_in && !n_out && f->st == TINFL_STATUS_NEEDS_MORE_INPUT) {
            f->st = TINFL_STATUS_FAILED; // no progress: give up rather than spin
        }
        in += n_in;
        left -= n_in;
        f->len += n_out;
    }
    return ESP_OK;
}

// Fetch one frame the newest document lists and no buffer holds, newest first so a fresh boot shows
// the current sky soonest, into a buffer the UI can't be drawing. A failed or short frame leaves its
// buffer empty: never published, and free for the next try.
static void fetch_frame(void)
{
    long long t = 0;
    for (int i = wx_shared.nframes - 1; i >= 0 && !t; i--) {
        if (wx_shared.frames[i] > 0 && holding(wx_shared.frames[i]) < 0) {
            t = wx_shared.frames[i];
        }
    }
    if (!t) {
        return;
    }
    xSemaphoreTake(wx_mutex, portMAX_DELAY);
    unsigned busy = pub_mask | ui_mask;
    xSemaphoreGive(wx_mutex);
    int b = 0;
    while (b < nbuf && (busy & 1u << b)) {
        b++;
    }
    if (b == nbuf) {
        return; // every buffer is in use until the UI takes the newest set
    }
    held[b] = 0;
    inflate_t f = {.out = (uint8_t *)fbuf[b], .cap = FRAME_BYTES, .st = TINFL_STATUS_NEEDS_MORE_INPUT};
    tinfl_init(&inflater);
    char path[48];
    snprintf(path, sizeof path, "/api/v1/weather/frame/%lld", t);
    int status = http(HTTP_METHOD_GET, path, on_frame, &f);
    if (status == 200 && f.st == TINFL_STATUS_DONE && f.len == FRAME_BYTES) {
        held[b] = t;
    } else {
        ESP_LOGW(TAG, "frame %lld: HTTP %d, inflate %d after %u bytes", t, status, f.st, (unsigned)f.len);
    }
}

// If the newest astro document names a sky picture the front buffer doesn't hold, download it into a buffer the
// UI can't be drawing and swap it in only when the status is 200, the inflate DONE and exactly SKY_BYTES arrived;
// anything less leaves the front picture untouched. The frame fetch and this one both run in the net task, one
// after the other, never concurrently, so they safely share the tinfl `inflater`.
static void fetch_sky(void)
{
    long long t = ax_shared.sky;
    if (t <= 0 || t == sky_t || (ax_status != 200 && ax_status != 304) || !nsky) {
        return;
    }
    xSemaphoreTake(wx_mutex, portMAX_DELAY);
    int ui = sky_ui;
    xSemaphoreGive(wx_mutex);
    int b = -1;
    uint16_t *dst = NULL, *temp = NULL;
    if (nsky == 2) {
        b = 0;
        while (b < 2 && (b == sky_front || b == ui)) {
            b++;
        }
        if (b == 2) {
            return; // the UI hasn't moved on to the newest picture yet
        }
        dst = sky_buf[b];
    } else {
        // Single-buffered: inflate into a temporary and copy over only when complete, so a partial picture is never
        // shown. The copy can tear a frame the UI is mid-way through drawing; the next redraw is whole.
        dst = temp = heap_caps_malloc(SKY_BYTES, MALLOC_CAP_SPIRAM);
        if (!temp) {
            ESP_LOGW(TAG, "sky: no memory for a temporary picture");
            return;
        }
    }
    inflate_t f = {.out = (uint8_t *)dst, .cap = SKY_BYTES, .deadline_us = esp_timer_get_time() + SKY_DL_US,
                   .st = TINFL_STATUS_NEEDS_MORE_INPUT};
    tinfl_init(&inflater);
    char path[48];
    snprintf(path, sizeof path, "/api/v1/astro/sky/%lld", t);
    int status = http(HTTP_METHOD_GET, path, on_frame, &f);
    if (status == 200 && f.st == TINFL_STATUS_DONE && f.len == SKY_BYTES) {
        astro_sky_fit(dst); // once per picture, before the UI or the SD copy sees it: a saved sky.raw is fitted
        xSemaphoreTake(wx_mutex, portMAX_DELAY);
        if (temp) {
            memcpy(sky_buf[0], temp, SKY_BYTES);
            b = 0;
        }
        sky_front = b;
        sky_t = t;
        sky_seq++;
        xSemaphoreGive(wx_mutex);
        if (sd_ok && (sky_saved_us < 0 || esp_timer_get_time() - sky_saved_us >= SKY_SAVE_US)) {
            sky_saved_us = esp_timer_get_time(); // a failed write waits too: spare a failing card
            feed(); // the download may have used much of the timeout: the 285 KB write gets its own
            save_atomic(SD_SKY, SD_SKY_TMP, (const char *)sky_buf[b], SKY_BYTES);
        }
    } else {
        ESP_LOGW(TAG, "sky %lld: HTTP %d, inflate %d after %u bytes", t, status, f.st, (unsigned)f.len);
    }
    heap_caps_free(temp); // NULL is fine
}

// Publish the newest document's fully inflated frames, oldest first, when that set changes. A document
// without frames (the aggregator just restarted) leaves the last set on screen. So does an outage that
// leaves us holding none of them, while a buffer is free; on the 5-buffer fallback no buffer is free, so
// the shown set loses its oldest frame instead (never an empty set), and the freed buffer refills. A
// shown set of one frame (only with a single buffer) gives way to the empty set, so it can't freeze.
static void publish_frames(void)
{
    if (!wx_shared.nframes) {
        return;
    }
    net_frames_t f = {0};
    unsigned mask = 0;
    for (int i = 0; i < wx_shared.nframes; i++) {
        int b = holding(wx_shared.frames[i]);
        if (b >= 0) {
            f.t[f.n] = held[b];
            f.px[f.n++] = fbuf[b];
            mask |= 1u << b;
        }
    }
    // Same buffers means the same frames: a published buffer is never refilled.
    xSemaphoreTake(wx_mutex, portMAX_DELAY);
    bool changed = mask != pub_mask && wx_frames_publish(f.n, pub_mask, pub_mask | ui_mask, nbuf);
    if (changed && !f.n) { // all busy: shed the oldest shown frame, once the UI has taken the current set
        // a shown set of 1 can't shed (with a single buffer it would freeze the radar for good): publish the empty set
        changed = frames_pub.n <= 1 || ui_mask == pub_mask;
        for (int i = 1; changed && i < frames_pub.n; i++) {
            f.t[f.n] = frames_pub.t[i];
            f.px[f.n++] = frames_pub.px[i];
        }
        for (int b = 0; changed && frames_pub.n > 1 && b < nbuf; b++) {
            if (fbuf[b] == frames_pub.px[0]) {
                mask = pub_mask & ~(1u << b);
            }
        }
    }
    if (changed) {
        frames_pub = f;
        pub_mask = mask;
        frames_seq++;
    }
    xSemaphoreGive(wx_mutex);
    static bool full;
    if (changed && f.n == WX_FRAMES && !full) {
        full = true;
        ESP_LOGI(TAG, "frames: first full set, PSRAM free %u KB",
                 (unsigned)(heap_caps_get_free_size(MALLOC_CAP_SPIRAM) / 1024));
    }
}

// GET one alert's full text into the slot net_alert_take hands out.
static void fetch_alert(int n, bool up)
{
    xSemaphoreTake(wx_mutex, portMAX_DELAY);
    alert_ready = false; // alert_body is about to be rewritten
    xSemaphoreGive(wx_mutex);
    char path[40];
    snprintf(path, sizeof path, "/api/v1/weather/alert/%d", n);
    body_t b = {.buf = alert_body, .cap = sizeof alert_body};
    int status = up ? http(HTTP_METHOD_GET, path, on_http, &b) : -1;
    alert_body[b.overflow ? 0 : b.len] = '\0'; // too big reads as unparsable, not as half a text
    xSemaphoreTake(wx_mutex, portMAX_DELAY);
    alert_n = n;
    alert_status = status;
    alert_ready = true;
    xSemaphoreGive(wx_mutex);
}

void net_log_heap(const char *why)
{
    ESP_LOGI(TAG, "%s: internal %u KB free, largest %u KB, min %u KB; PSRAM %u KB", why,
             (unsigned)(heap_caps_get_free_size(MALLOC_CAP_INTERNAL) / 1024),
             (unsigned)(heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL) / 1024),
             (unsigned)(heap_caps_get_minimum_free_size(MALLOC_CAP_INTERNAL) / 1024),
             (unsigned)(heap_caps_get_free_size(MALLOC_CAP_SPIRAM) / 1024));
}

static void *psram_malloc(size_t n) { return heap_caps_malloc(n, MALLOC_CAP_SPIRAM); }

static void net_task(void *arg)
{
    esp_http_client_config_t cfg = {.timeout_ms = 4000, .event_handler = on_http, .keep_alive_enable = true};
    esp_http_client_handle_t c = poll_client(cfg, "/api/v1/panel", &pb);
    esp_http_client_handle_t w = poll_client(cfg, "/api/v1/weather", &wb);
    esp_http_client_handle_t l = poll_client(cfg, "/api/v1/layout", &lb);
    esp_http_client_handle_t x = poll_client(cfg, "/api/v1/cards", &cb);
    esp_http_client_handle_t a = poll_client(cfg, "/api/v1/astro", &ab);
    if (!c || !w || !l || !x || !a) {
        ESP_LOGE(TAG, "AGGREGATOR_URL isn't usable: not polling");
        vTaskDelete(NULL);
    }
    // Restart on a hang (120 s, main.c). Only after the check above: a task that deletes itself while
    // subscribed trips the watchdog. Fed before every request (http, get_doc), as a download arrives (on_frame), and
    // below; the longest legitimate stretch between feeds is one request plus its SD write, well under 120 s.
    watched = esp_task_wdt_add(NULL) == ESP_OK;
    esp_http_client_set_header(c, "X-Panel-Boot", boot_hdr);

    int wait_ms = POLL_MS;
    int64_t wx_due_us = 0, lx_due_us = 0, cx_due_us = 0, ax_due_us = 0;
    int cx_back_ms = POLL_MS; // the cards poll's own back-off: it never touches ok or wait_ms
    int ax_back_ms = POLL_MS; // likewise the astro poll's
    int64_t heap_due_us = 30LL * 1000 * 1000; // once ~30 s after boot, then every 10 min
    for (;;) {
        if (esp_timer_get_time() >= heap_due_us) {
            net_log_heap("heap");
            ESP_LOGI(TAG, "net task: %u of %u B stack never used", (unsigned)uxTaskGetStackHighWaterMark(NULL), NET_STACK);
            heap_due_us = esp_timer_get_time() + 10LL * 60 * 1000 * 1000;
        }
        wifi_ap_record_t ap;
        bool up = net_status().state == NET_UP;
        int rssi = (up && esp_wifi_sta_get_ap_info(&ap) == ESP_OK) ? ap.rssi : 0;
        taskENTER_CRITICAL(&lock);
        st.rssi = rssi;
        taskEXIT_CRITICAL(&lock);

        if (up) {
            bool ok = poll_panel(c);
            if (ok) {
                taskENTER_CRITICAL(&lock);
                fetched_us = esp_timer_get_time();
                taskEXIT_CRITICAL(&lock);
                // Weather only follows a good panel poll, so it shares the panel's back-off.
                if (esp_timer_get_time() >= wx_due_us) {
                    wx_due_us = esp_timer_get_time() + WX_POLL_MS * 1000LL;
                    ok = poll_weather(w);
                }
                if (ok && (wx_status == 200 || wx_status == 304)) { // wx_status: this task writes it
                    fetch_frame(); // one per pass, so taps never wait behind a whole set
                    publish_frames();
                }
                if (ok && esp_timer_get_time() >= lx_due_us) {
                    lx_due_us = esp_timer_get_time() + LX_POLL_MS * 1000LL;
                    ok = poll_layout(l);
                }
                // Cards go last, on their own due time and back-off, so a slow /cards delays no other poll.
                if (esp_timer_get_time() >= cx_due_us) {
                    bool cok = poll_cards(x);
                    cx_back_ms = cok ? POLL_MS : (cx_back_ms * 2 < POLL_MAX_MS ? cx_back_ms * 2 : POLL_MAX_MS);
                    cx_due_us = esp_timer_get_time() + (cx_status == 404 ? 60000 : cx_back_ms) * 1000LL;
                }
                // Astro goes after the weather poll, the frame fetch and the cards, on its own due time and back-off
                // (never ok/wait_ms), so a slow /astro or sky download delays no other poll by more than its own time.
                if (esp_timer_get_time() >= ax_due_us) {
                    bool aok = poll_astro(a);
                    ax_back_ms = aok ? POLL_MS : (ax_back_ms * 2 < POLL_MAX_MS ? ax_back_ms * 2 : POLL_MAX_MS);
                    ax_due_us = esp_timer_get_time() + (!aok ? ax_back_ms : ax_status == 404 ? AX_NOLOC_MS : AX_POLL_MS) * 1000LL;
                    fetch_sky();
                }
            }
            wait_ms = ok ? POLL_MS : (wait_ms * 2 < POLL_MAX_MS ? wait_ms * 2 : POLL_MAX_MS); // aggregator down: back off
        }
        // Sleep until the next poll, but serve taps (card done, notes read, alert text) the moment they arrive.
        req_t q;
        TickType_t wait = pdMS_TO_TICKS(up ? wait_ms : 1000);
        feed(); // the wait is at most POLL_MAX_MS (30 s)
        while (xQueueReceive(req_q, &q, wait) == pdTRUE) {
            feed(); // taps can keep this loop going (with Wi-Fi down they post nothing)
            if (q.kind == 'a') {
                fetch_alert(atoi(q.id), up);
            } else {
                char path[80];
                snprintf(path, sizeof path, q.kind == 'd' ? "/api/v1/planka/cards/%s/done" : "/api/v1/notify/%s/read", q.id);
                int status = up ? http(HTTP_METHOD_POST, path, NULL, NULL) : -1;
                if (q.kind == 'd') {
                    net_done_t r = {.status = status};
                    strlcpy(r.card_id, q.id, sizeof r.card_id);
                    xQueueSend(result_q, &r, 0);
                }
            }
            wait = pdMS_TO_TICKS(1500); // then re-poll soon so the change comes back in the data
        }
    }
}

void net_start(void)
{
    panel_mutex = xSemaphoreCreateMutex();
    wx_mutex = xSemaphoreCreateMutex();
    req_q = xQueueCreate(8, sizeof(req_t));
    result_q = xQueueCreate(4, sizeof(net_done_t));
    time_t t;
    if (board_rtc_read(&t)) {
        settimeofday(&(struct timeval){.tv_sec = t}, NULL);
        st.time_ok = true;
        ESP_LOGI(TAG, "clock set from RTC");
    } else {
        ESP_LOGW(TAG, "RTC has no valid time; waiting for NTP");
    }
    cJSON_InitHooks(&(cJSON_Hooks){.malloc_fn = psram_malloc, .free_fn = free}); // keep internal RAM for Wi-Fi
    // Sky buffers (A5), first so the SD picture has somewhere to load; PSRAM is still in one piece.
    while (nsky < 2 && (sky_buf[nsky] = heap_caps_malloc(SKY_BYTES, MALLOC_CAP_SPIRAM))) {
        nsky++;
    }
    ESP_LOGI(TAG, "sky: %d buffers of %u KB, PSRAM free %u KB%s", nsky, (unsigned)(SKY_BYTES / 1024),
             (unsigned)(heap_caps_get_free_size(MALLOC_CAP_SPIRAM) / 1024), nsky < 2 ? " (single-buffered)" : "");
    sd_ok = board_sd_mount(SD_MOUNT);
    settings_load(); // before the SD loads (FAT file times are local), boot_record, Wi-Fi and SNTP
    setenv("TZ", settings.tz, 1);
    tzset();
    if (sd_ok) {
        load_layout_copy();
        load_cards_copy();
        load_astro_copy();
        load_sky_copy();
        load_copy(); // after the clock is set, so the copy's age is right
    }
    boot_record(); // also with Wi-Fi off
    if (!settings.wifi_ssid[0]) {
        ESP_LOGW(TAG, "WIFI_SSID is empty: Wi-Fi off");
        return;
    }

    // Radar frame buffers, taken once at boot while PSRAM is still in one piece.
    while (nbuf < FRAME_BUFS && (fbuf[nbuf] = heap_caps_malloc(FRAME_BYTES, MALLOC_CAP_SPIRAM))) {
        nbuf++;
    }
    size_t spare = heap_caps_get_free_size(MALLOC_CAP_SPIRAM);
    ESP_LOGI(TAG, "frames: %d buffers, PSRAM free %u KB", nbuf, (unsigned)(spare / 1024));
    if (nbuf == FRAME_BUFS && spare < PSRAM_SPARE) {
        heap_caps_free(fbuf[--nbuf]); // the loop shows 4 frames while one is replaced
        ESP_LOGW(TAG, "frames: 5 buffers, PSRAM free %u KB",
                 (unsigned)(heap_caps_get_free_size(MALLOC_CAP_SPIRAM) / 1024));
    }

    esp_err_t err = nvs_flash_init(); // the Wi-Fi driver needs NVS even with RAM storage
    if (err == ESP_ERR_NVS_NO_FREE_PAGES || err == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        err = nvs_flash_init();
    }
    ESP_ERROR_CHECK(err);
    ESP_ERROR_CHECK(esp_netif_init());
    ESP_ERROR_CHECK(esp_event_loop_create_default());
    esp_netif_create_default_wifi_sta();
    wifi_init_config_t init = WIFI_INIT_CONFIG_DEFAULT();
    ESP_ERROR_CHECK(esp_wifi_init(&init));
    ESP_ERROR_CHECK(esp_wifi_set_storage(WIFI_STORAGE_RAM)); // no flash writes: they make the RGB panel drift
    ESP_ERROR_CHECK(esp_event_handler_register(WIFI_EVENT, ESP_EVENT_ANY_ID, on_event, NULL));
    ESP_ERROR_CHECK(esp_event_handler_register(IP_EVENT, IP_EVENT_STA_GOT_IP, on_event, NULL));
    const esp_timer_create_args_t targs = {.callback = retry, .name = "wifi_retry"};
    ESP_ERROR_CHECK(esp_timer_create(&targs, &retry_timer));

    wifi_config_t wc = {.sta = {.scan_method = WIFI_ALL_CHANNEL_SCAN, .sort_method = WIFI_CONNECT_AP_BY_SIGNAL}};
    strlcpy((char *)wc.sta.ssid, settings.wifi_ssid, sizeof wc.sta.ssid);
    strlcpy((char *)wc.sta.password, settings.wifi_password, sizeof wc.sta.password);
    ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_STA));
    ESP_ERROR_CHECK(esp_wifi_set_config(WIFI_IF_STA, &wc));
    ESP_ERROR_CHECK(esp_wifi_start());
    esp_wifi_set_ps(WIFI_PS_NONE); // mains powered; keeps taps-to-POST snappy

    esp_sntp_config_t sntp = ESP_NETIF_SNTP_DEFAULT_CONFIG(settings.ntp_server); // lwIP keeps the pointer: settings is static
    sntp.sync_cb = on_time_sync;
    ESP_ERROR_CHECK(esp_netif_sntp_init(&sntp));

    xTaskCreatePinnedToCore(net_task, "net", NET_STACK, NULL, 5, NULL, 0);
}
