if(CMAKE_BUILD_EARLY_EXPANSION) # IDF first reads this file as a script, where CONFIGURE_DEPENDS is invalid
    file(GLOB srcs "*.c")
else()
    file(GLOB srcs CONFIGURE_DEPENDS "*.c") # re-globbed each build: a new .c needs no `build.sh reconfigure`
endif()
idf_component_register(SRCS ${srcs}
                       INCLUDE_DIRS "."
                       EMBED_FILES "fonts/Montserrat-Medium.ttf" "fonts/Montserrat-ExtraBold-sub.ttf"
                                   "fonts/Montserrat-SemiBold-sub.ttf" "startup_logo.rgb565"
                       REQUIRES esp_driver_i2c esp_driver_gpio esp_lcd esp_wifi esp_netif esp_event
                                nvs_flash esp_timer lwip esp_http_client json fatfs sdmmc esp_driver_sdspi
                                esp_driver_spi vfs)

# Skip ESP-IDF's SDIO probes during SD init: see __wrap_sdmmc_init_io in board.c.
target_link_libraries(${COMPONENT_LIB} INTERFACE "-Wl,--wrap=sdmmc_io_reset" "-Wl,--wrap=sdmmc_init_io")

# The deliberate-hang test (README "Self-restart"): sh build.sh -DDESKPANEL_WDT_TEST=1 build. CMake keeps the value:
# build with -DDESKPANEL_WDT_TEST=0 to drop it again.
if(DESKPANEL_WDT_TEST)
    target_compile_definitions(${COMPONENT_LIB} PRIVATE DESKPANEL_WDT_TEST)
endif()

# The generic firmware for the flasher page (release.sh, its own build-release/): net.c includes secrets.example.h,
# never secrets.h, so the image holds no Wi-Fi, address or token; settings.txt on the SD card sets them.
if(DESKPANEL_GENERIC)
    target_compile_definitions(${COMPONENT_LIB} PRIVATE DESKPANEL_GENERIC)
endif()
