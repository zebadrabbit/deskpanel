#!/usr/bin/env python3
"""Turn the simulator's PPM frames into PNGs plus an animated PNG tour. Standard library only.
    png.py <frames dir> <out dir>      frames are NN-name.ppm; writes name.png and tour.png"""
import glob
import os
import struct
import sys
import zlib

SIG = b"\x89PNG\r\n\x1a\n"


def chunk(kind, data):
    return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)


def read_ppm(path):
    with open(path, "rb") as f:
        magic, size, maxval, rgb = f.read().split(b"\n", 3)
    w, h = map(int, size.split())
    assert magic == b"P6" and maxval == b"255" and len(rgb) == w * h * 3, path
    return w, h, rgb


def pixels(w, h, rgb):  # each scanline gets filter byte 0
    return zlib.compress(b"".join(b"\0" + rgb[y * w * 3:(y + 1) * w * 3] for y in range(h)), 9)


def ihdr(w, h):
    return chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0))


def png(w, h, rgb):
    return SIG + ihdr(w, h) + chunk(b"IDAT", pixels(w, h, rgb)) + chunk(b"IEND", b"")


def apng(frames, delay_ms):
    w, h, _ = frames[0]
    out = [SIG, ihdr(w, h), chunk(b"acTL", struct.pack(">II", len(frames), 0))]
    seq = 0
    for i, (_, _, rgb) in enumerate(frames):
        out.append(chunk(b"fcTL", struct.pack(">IIIIIHHBB", seq, w, h, 0, 0, delay_ms, 1000, 0, 0)))
        seq += 1
        data = pixels(w, h, rgb)
        if i == 0:
            out.append(chunk(b"IDAT", data))
        else:
            out.append(chunk(b"fdAT", struct.pack(">I", seq) + data))
            seq += 1
    out.append(chunk(b"IEND", b""))
    return b"".join(out)


def main(src, dst):
    os.makedirs(dst, exist_ok=True)
    frames = []
    for path in sorted(glob.glob(os.path.join(src, "*.ppm"))):
        frame = read_ppm(path)
        frames.append(frame)
        name = os.path.basename(path)[3:-4]  # strip "NN-" and ".ppm"
        with open(os.path.join(dst, name + ".png"), "wb") as f:
            f.write(png(*frame))
    with open(os.path.join(dst, "tour.png"), "wb") as f:
        f.write(apng(frames, 2200))
    print(f"wrote {len(frames)} screenshots + tour.png to {dst}")


if __name__ == "__main__":
    main(*sys.argv[1:3])
