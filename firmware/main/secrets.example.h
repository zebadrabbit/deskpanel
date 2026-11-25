// Copy to secrets.h (gitignored) and fill in.
// Any of these can also be changed without rebuilding: a KEY=value line in settings.txt on the SD card (README, "Change the panel's Wi-Fi…").
#pragma once

#define WIFI_SSID      ""   // 2.4 GHz network that can reach the aggregator; "" = Wi-Fi off
#define WIFI_PASSWORD  ""

#define AGGREGATOR_URL "http://192.0.2.10:8088" // placeholder (documentation range): your aggregator's LAN IP
#define PANEL_TOKEN    ""   // PANEL_TOKEN from aggregator/.env

#define DESKPANEL_TZ   "CST6CDT,M3.2.0,M11.1.0" // POSIX TZ for the clock
#define NTP_SERVER     "pool.ntp.org"
