#!/bin/sh
# The generic firmware for the flasher page (../web/): built from secrets.example.h (Wi-Fi off, placeholder address,
# no token; a settings.txt on the SD card sets them), never secrets.h. Its own build dir and sdkconfig, so build/ and
# sdkconfig (yours, what flash.ps1 writes) are never touched. Needs only Docker, like build.sh.
# Output: ../web/firmware/deskpanel-<version>.bin, one image for offset 0, ../web/manifest.json pointing at it, and
# the page's two simulator screenshots in ../web/img/ (all gitignored; .github/workflows/pages.yml publishes web/).
set -e
cd "$(dirname "$0")"
ver=$(git describe --tags --always --dirty 2>/dev/null || echo "dev-$(date -u +%Y%m%d)")
sh build.sh -B build-release -D SDKCONFIG=/project/build-release/sdkconfig -D DESKPANEL_GENERIC=1 build merge-bin
rm -rf ../web/firmware
mkdir -p ../web/firmware
cp build-release/merged-binary.bin "../web/firmware/deskpanel-$ver.bin"
# ESP Web Tools: no Improv Wi-Fi setup to wait for; a new install erases the flash first (its default).
cat >../web/manifest.json <<EOF
{
  "name": "deskpanel",
  "version": "$ver",
  "new_install_improv_wait_time": 0,
  "builds": [{"chipFamily": "ESP32-S3", "parts": [{"path": "firmware/deskpanel-$ver.bin", "offset": 0}]}]
}
EOF
rm -rf ../web/img
mkdir -p ../web/img
cp ../docs/screenshots/tour.png ../docs/screenshots/first-boot.png ../web/img/
ls -l ../web/firmware ../web/manifest.json ../web/img
