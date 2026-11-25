#!/usr/bin/env python3
"""Pixel-compare two simulator frame dirs. Standard library only.
    compare.py <dirA> <dirB> [name...]   names are NN-name or name (.ppm optional); frames match by bare name (NN- stripped); default:
all common frames, including unset/ and layout/ ones (named unset/<name>, layout/<name>)
Prints `name: identical` or `name: N pixels differ (bbox x0,y0-x1,y1)`; exits 1 if any differ."""
import glob
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from png import read_ppm  # noqa: E402


def bare(p):  # "dir/04-planka.ppm" -> "planka"; frames in unset/ come out as "unset/weather-unset"
    return re.sub(r"^\d+-", "", os.path.basename(p)[:-4])


def frames(d):
    out = {}
    for sub in ("", "unset/", "layout/"):  # frames of a sub-run keep its dir in their name
        for p in glob.glob(os.path.join(d, sub, "*.ppm")):
            k = sub + bare(p)
            if k in out:
                sys.exit(f"{d}: frame name {k!r} occurs twice")
            out[k] = p
    return out


def diff(a, b):
    (w, h, x), (w2, h2, y) = read_ppm(a), read_ppm(b)
    if (w, h) != (w2, h2):
        return f"size differs ({w}x{h} vs {w2}x{h2})"
    if x == y:
        return None
    n, x0, y0, x1, y1 = 0, w, h, -1, -1
    for r in range(h):  # skip equal rows fast
        ra, rb = x[r * w * 3:(r + 1) * w * 3], y[r * w * 3:(r + 1) * w * 3]
        if ra == rb:
            continue
        for c in range(w):
            if ra[c * 3:c * 3 + 3] != rb[c * 3:c * 3 + 3]:
                n += 1
                x0, x1, y0, y1 = min(x0, c), max(x1, c), min(y0, r), max(y1, r)
    return f"{n} pixels differ (bbox {x0},{y0}-{x1},{y1})"


def main(da, db, names):
    fa, fb = frames(da), frames(db)
    keys = sorted(fa.keys() & fb.keys()) if not names else [
        re.sub(r"^\d+-", "", n.removesuffix(".ppm")) for n in names]
    bad = 0
    for k in keys:
        if k not in fa or k not in fb:
            res = "missing in " + ("A" if k not in fa else "B")
        else:
            res = diff(fa[k], fb[k])
        bad += res is not None
        print(f"{k}: {res or 'identical'}")
    return 1 if bad or not keys else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1], sys.argv[2], sys.argv[3:]))
