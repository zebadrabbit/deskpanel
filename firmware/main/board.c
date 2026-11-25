// Board support for the Waveshare ESP32-S3-Touch-LCD-4.3B. Pins, RGB timings and the CH422G and
// GT911 sequences come from Waveshare's official 4.3B demo (ESP32-S3-Touch-LCD-4.3B-BOX-Demo,
// ESP-IDF/08_lvgl_Porting/main/waveshare_rgb_lcd_port.[ch]) and the board schematic. The demo's
// legacy I2C driver is replaced by i2c_master (the two can't be mixed in one build).
#include "board.h"

#include "driver/gpio.h"
#include "driver/i2c_master.h"
#include "driver/sdspi_host.h"
#include "esp_vfs_fat.h"
#include "sdmmc_cmd.h"
#include "esp_lcd_panel_ops.h"
#include "esp_lcd_panel_rgb.h"
#include "esp_lcd_touch_gt911.h"
#include "esp_log.h"
#include "esp_lvgl_port.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

static const char *TAG = "board";

// I2C0 is shared by the CH422G, the GT911 (0x5D) and the PCF85063A RTC (0x51).
#define PIN_I2C_SDA   GPIO_NUM_8
#define PIN_I2C_SCL   GPIO_NUM_9
#define PIN_TOUCH_INT GPIO_NUM_4
#define I2C_HZ        400000
#define RTC_ADDR      0x51

// SD card on SPI2 (Waveshare 4.3B demo, ESP-IDF/03_SD_Test/sdkconfig.defaults: MOSI 11, MISO 13,
// CLK 12, CS -1). CS is CH422G EXIO4, held low while mounted: the card is the bus's only device.
#define PIN_SD_MOSI GPIO_NUM_11
#define PIN_SD_MISO GPIO_NUM_13
#define PIN_SD_CLK  GPIO_NUM_12

// CH422G: each I2C "address" is a command. 0x24 sets the mode, 0x38 writes IO0-7.
#define CH422G_MODE      0x24
#define CH422G_OUT       0x38
#define CH422G_IO_OUTPUT 0x01
// EXIO wiring on the 4.3B: 1 touch reset, 2 backlight + panel DISP, 3 LCD reset, 4 SD CS (low = selected).
// EXIO0/EXIO5 are the isolated digital inputs; driving them low never fights the optocouplers.
#define EXIO_TP_RST  (1 << 1)
#define EXIO_BL      (1 << 2)
#define EXIO_LCD_RST (1 << 3)
#define EXIO_SD_CS   (1 << 4)

static i2c_master_bus_handle_t bus;
static i2c_master_dev_handle_t ch422_mode, ch422_out, rtc;
static uint8_t exio = EXIO_TP_RST | EXIO_LCD_RST | EXIO_SD_CS; // backlight off until the first frame

static i2c_master_dev_handle_t add_dev(uint16_t addr)
{
    i2c_device_config_t cfg = {
        .dev_addr_length = I2C_ADDR_BIT_LEN_7,
        .device_address = addr,
        .scl_speed_hz = I2C_HZ,
    };
    i2c_master_dev_handle_t dev;
    ESP_ERROR_CHECK(i2c_master_bus_add_device(bus, &cfg, &dev));
    return dev;
}

static void exio_write(void)
{
    ESP_ERROR_CHECK(i2c_master_transmit(ch422_out, &exio, 1, 100));
}

void board_backlight(bool on)
{
    exio = on ? (exio | EXIO_BL) : (exio & ~EXIO_BL);
    exio_write();
}

// The GT911 latches its I2C address from INT while RST rises: INT held low selects 0x5D.
static void touch_reset(void)
{
    gpio_config_t io = {.pin_bit_mask = 1ULL << PIN_TOUCH_INT, .mode = GPIO_MODE_OUTPUT};
    ESP_ERROR_CHECK(gpio_config(&io));
    gpio_set_level(PIN_TOUCH_INT, 0);
    exio &= ~EXIO_TP_RST;
    exio_write();
    vTaskDelay(pdMS_TO_TICKS(20));
    exio |= EXIO_TP_RST;
    exio_write();
    vTaskDelay(pdMS_TO_TICKS(10));
    gpio_set_direction(PIN_TOUCH_INT, GPIO_MODE_INPUT); // hand INT back to the GT911; we poll
    vTaskDelay(pdMS_TO_TICKS(60));
}

static esp_lcd_panel_handle_t panel_init(void)
{
    esp_lcd_rgb_panel_config_t cfg = {
        .clk_src = LCD_CLK_SRC_DEFAULT,
        .timings = {
            .pclk_hz = 16 * 1000 * 1000, // Waveshare's 800x480 value; higher drifts under PSRAM load
            .h_res = BOARD_LCD_H_RES,
            .v_res = BOARD_LCD_V_RES,
            .hsync_pulse_width = 4,
            .hsync_back_porch = 8,
            .hsync_front_porch = 8,
            .vsync_pulse_width = 4,
            .vsync_back_porch = 8,
            .vsync_front_porch = 8,
            .flags.pclk_active_neg = 1,
        },
        .data_width = 16,
        .bits_per_pixel = 16,
        .num_fbs = 2,                                  // LVGL renders into one while the other scans out
        .bounce_buffer_size_px = BOARD_LCD_H_RES * 10, // DMA reads internal RAM, so PSRAM stalls don't tear
        .dma_burst_size = 64,
        .hsync_gpio_num = 46,
        .vsync_gpio_num = 3,
        .de_gpio_num = 5,
        .pclk_gpio_num = 7,
        .disp_gpio_num = -1, // DISP is CH422G EXIO2, shared with the backlight
        // B3-B7, G2-G7, R3-R7
        .data_gpio_nums = {14, 38, 18, 17, 10, 39, 0, 45, 48, 47, 21, 1, 2, 42, 41, 40},
        .flags.fb_in_psram = 1,
    };
    esp_lcd_panel_handle_t panel;
    ESP_ERROR_CHECK(esp_lcd_new_rgb_panel(&cfg, &panel));
    ESP_ERROR_CHECK(esp_lcd_panel_init(panel));
    return panel;
}

static esp_lcd_touch_handle_t touch_init(void)
{
    touch_reset();
    esp_lcd_panel_io_handle_t io;
    esp_lcd_panel_io_i2c_config_t io_cfg = ESP_LCD_TOUCH_IO_I2C_GT911_CONFIG();
    io_cfg.scl_speed_hz = I2C_HZ;
    ESP_ERROR_CHECK(esp_lcd_new_panel_io_i2c(bus, &io_cfg, &io));
    esp_lcd_touch_config_t cfg = {
        .x_max = BOARD_LCD_H_RES,
        .y_max = BOARD_LCD_V_RES,
        .rst_gpio_num = GPIO_NUM_NC, // reset is on the CH422G, done above
        .int_gpio_num = GPIO_NUM_NC, // polled by the LVGL task
    };
    esp_lcd_touch_handle_t tp;
    ESP_ERROR_CHECK(esp_lcd_touch_new_i2c_gt911(io, &cfg, &tp));
    return tp;
}

lv_display_t *board_init(void)
{
    i2c_master_bus_config_t bus_cfg = {
        .i2c_port = I2C_NUM_0,
        .sda_io_num = PIN_I2C_SDA,
        .scl_io_num = PIN_I2C_SCL,
        .clk_source = I2C_CLK_SRC_DEFAULT,
        .glitch_ignore_cnt = 7,
        .flags.enable_internal_pullup = true,
    };
    ESP_ERROR_CHECK(i2c_new_master_bus(&bus_cfg, &bus));
    ch422_mode = add_dev(CH422G_MODE);
    ch422_out = add_dev(CH422G_OUT);
    rtc = add_dev(RTC_ADDR);
    uint8_t mode = CH422G_IO_OUTPUT;
    ESP_ERROR_CHECK(i2c_master_transmit(ch422_mode, &mode, 1, 100));
    exio_write(); // backlight off, resets released, SD deselected

    esp_lcd_panel_handle_t panel = panel_init();
    esp_lcd_touch_handle_t tp = touch_init();

    lvgl_port_cfg_t port_cfg = ESP_LVGL_PORT_INIT_CONFIG();
    port_cfg.task_affinity = 1; // Wi-Fi lives on core 0
    port_cfg.task_stack = 16384; // flex layout + arc drawing + float printf; 8 KB overflowed
    ESP_ERROR_CHECK(lvgl_port_init(&port_cfg));

    lvgl_port_display_cfg_t disp_cfg = {
        .panel_handle = panel,
        .buffer_size = BOARD_LCD_H_RES * BOARD_LCD_V_RES,
        .double_buffer = true,
        .hres = BOARD_LCD_H_RES,
        .vres = BOARD_LCD_V_RES,
        .color_format = LV_COLOR_FORMAT_RGB565,
        .flags = {.buff_spiram = true, .direct_mode = true},
    };
    // Draw straight into the panel's two PSRAM framebuffers and swap on vsync: no tearing, no copy.
    lvgl_port_display_rgb_cfg_t rgb_cfg = {.flags = {.bb_mode = true, .avoid_tearing = true}};
    lv_display_t *disp = lvgl_port_add_disp_rgb(&disp_cfg, &rgb_cfg);
    lvgl_port_touch_cfg_t touch_cfg = {.disp = disp, .handle = tp};
    lvgl_port_add_touch(&touch_cfg);
    ESP_LOGI(TAG, "panel %dx%d + GT911 ready", BOARD_LCD_H_RES, BOARD_LCD_V_RES);
    return disp;
}

// ESP-IDF's card init always probes for SDIO first (CMD52, then CMD5). A memory card times out on
// CMD5, and normally the driver recovers by deselecting the card. Here CS sits on the IO expander,
// held low for good, so the late reply stays on the line and is read as the answer to the next
// command (CMD59 then "fails" and the mount aborts). This slot only ever holds memory cards, so the
// linker (--wrap, main/CMakeLists.txt) replaces both probes with the outcome a memory card produces.
esp_err_t __wrap_sdmmc_io_reset(sdmmc_card_t *card) { return ESP_OK; }

esp_err_t __wrap_sdmmc_init_io(sdmmc_card_t *card)
{
    card->is_sdio = 0;
    card->is_mem = 1;
    return ESP_OK;
}

bool board_sd_mount(const char *mount_point)
{
    exio &= ~EXIO_SD_CS; // select the card for good
    exio_write();
    sdmmc_host_t host = SDSPI_HOST_DEFAULT();
    spi_bus_config_t bus_cfg = {
        .mosi_io_num = PIN_SD_MOSI,
        .miso_io_num = PIN_SD_MISO,
        .sclk_io_num = PIN_SD_CLK,
        .quadwp_io_num = -1,
        .quadhd_io_num = -1,
        .max_transfer_sz = 4000,
    };
    esp_err_t err = spi_bus_initialize(host.slot, &bus_cfg, SDSPI_DEFAULT_DMA);
    if (err == ESP_OK) {
        sdspi_device_config_t slot = SDSPI_DEVICE_CONFIG_DEFAULT();
        slot.gpio_cs = GPIO_NUM_NC; // CS is on the expander, already low
        slot.host_id = host.slot;
        esp_vfs_fat_sdmmc_mount_config_t mount_cfg = {
            .format_if_mount_failed = false, // never wipe whatever is on the card
            .max_files = 2,
            .allocation_unit_size = 16 * 1024,
        };
        sdmmc_card_t *card;
        err = esp_vfs_fat_sdspi_mount(mount_point, &host, &slot, &mount_cfg, &card);
        if (err == ESP_OK) {
            ESP_LOGI(TAG, "SD card \"%s\" mounted at %s, %llu MB", card->cid.name, mount_point,
                     (unsigned long long)card->csd.capacity * card->csd.sector_size / (1024 * 1024));
            return true;
        }
        spi_bus_free(host.slot);
    }
    ESP_LOGW(TAG, "no usable SD card (%s); running without the saved copy", esp_err_to_name(err));
    exio |= EXIO_SD_CS;
    exio_write();
    return false;
}

static int from_bcd(uint8_t v) { return (v >> 4) * 10 + (v & 0x0f); }
static uint8_t to_bcd(int v) { return (uint8_t)(((v / 10) << 4) | (v % 10)); }

// Days since 1970-01-01 for a civil date (newlib has no timegm). Howard Hinnant's algorithm.
static int64_t days_from_civil(int y, unsigned m, unsigned d)
{
    y -= m <= 2;
    int64_t era = (y >= 0 ? y : y - 399) / 400;
    unsigned yoe = (unsigned)(y - era * 400);
    unsigned doy = (153 * (m > 2 ? m - 3 : m + 9) + 2) / 5 + d - 1;
    unsigned doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    return era * 146097 + (int64_t)doe - 719468;
}

bool board_rtc_read(time_t *out)
{
    uint8_t reg = 0x04, r[7]; // seconds..years
    if (i2c_master_transmit_receive(rtc, &reg, 1, r, sizeof r, 100) != ESP_OK) {
        return false;
    }
    if (r[0] & 0x80) { // OS flag: the oscillator stopped, the time is junk
        return false;
    }
    int year = 2000 + from_bcd(r[6]);
    if (year < 2025) {
        return false;
    }
    int64_t days = days_from_civil(year, from_bcd(r[5] & 0x1f), from_bcd(r[3] & 0x3f));
    *out = (time_t)(days * 86400 + from_bcd(r[2] & 0x3f) * 3600 + from_bcd(r[1] & 0x7f) * 60 + from_bcd(r[0] & 0x7f));
    return true;
}

void board_rtc_write(time_t t)
{
    struct tm tm;
    gmtime_r(&t, &tm);
    // Writing the seconds register also clears the OS flag.
    uint8_t w[8] = {0x04, to_bcd(tm.tm_sec), to_bcd(tm.tm_min), to_bcd(tm.tm_hour), to_bcd(tm.tm_mday),
                    (uint8_t)tm.tm_wday, to_bcd(tm.tm_mon + 1), to_bcd(tm.tm_year - 100)};
    if (i2c_master_transmit(rtc, w, sizeof w, 100) != ESP_OK) {
        ESP_LOGW(TAG, "RTC write failed");
    }
}
