// PC simulator stand-in: no task watchdog on a PC.
#pragma once
#define ESP_OK 0
static inline int esp_task_wdt_add(void *task) { (void)task; return ESP_OK; }
static inline int esp_task_wdt_reset(void) { return ESP_OK; }
