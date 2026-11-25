// deskpanel firmware: a thin client for the deskpanel aggregator. It renders and sends touch
// actions; every API call, credential and data shaping lives in the aggregator.
#include "board.h"
#include "esp_lvgl_port.h"
#include "esp_task_wdt.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "net.h"
#include "ui.h"

void app_main(void)
{
    // Restart on a hang (README "Self-restart"): 120 s for the UI, net and both idle tasks, panic as in sdkconfig.defaults,
    // whose timeout Kconfig caps at 60 s. A failure here leaves those 60 s.
    esp_task_wdt_reconfigure(&(esp_task_wdt_config_t){.timeout_ms = 120 * 1000, .idle_core_mask = 3, .trigger_panic = true});
    lv_display_t *disp = board_init();
    net_start(); // RTC -> system clock first, so the first frame already shows the time
    lvgl_port_lock(0);
    ui_init(disp);
    lvgl_port_unlock();
    vTaskDelay(pdMS_TO_TICKS(150)); // let the first frame reach the panel before lighting it
    board_backlight(true);
}
