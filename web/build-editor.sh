#!/bin/sh
# The screen editor's public demo for the flasher page: web/editor/ (gitignored) is the real page from
# aggregator/editor/ with backend.demo.js in place of backend.js, so it reads demo-data.json and downloads
# layout.json instead of saving. Run by .github/workflows/pages.yml; safe to run by hand.
# Every file is copied by name, so one that goes missing (or is renamed) stops the build instead of publishing
# a page that can't load. SRC is overridable only so the check can point it at a copy.
set -eu
cd "$(dirname "$0")"
SRC=${SRC:-../aggregator/editor}
rm -rf editor
mkdir editor
for f in index.html editor.css editor.js model.js demo-data.json; do
  cp "$SRC/$f" "editor/$f"
done
cp "$SRC/backend.demo.js" editor/backend.js
ls -l editor
