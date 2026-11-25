// PC simulator stand-in: microseconds of simulated time (sim.c drives the clock).
#pragma once
#include <stdint.h>
int64_t esp_timer_get_time(void);
