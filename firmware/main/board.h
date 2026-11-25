// Board support: Waveshare ESP32-S3-Touch-LCD-4.3B.
#pragma once

#include <stdbool.h>
#include <time.h>

#include "lvgl.h"

#define BOARD_LCD_H_RES 800
#define BOARD_LCD_V_RES 480

// I2C bus, CH422G expander, RGB panel, GT911 touch and the LVGL port. Backlight stays off.
lv_display_t *board_init(void);
void board_backlight(bool on); // on/off only: the backlight enable is a CH422G pin, no PWM path

// Mount the SD card's FAT filesystem at `mount_point`. False (and logged) if there's no usable card;
// the card is never formatted.
bool board_sd_mount(const char *mount_point);

// PCF85063A RTC, kept in UTC. read() is false if the RTC lost power or was never set.
bool board_rtc_read(time_t *out);
void board_rtc_write(time_t t);
