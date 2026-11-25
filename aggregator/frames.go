package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"image"
	"io"
)

// Frame encoding for the panel: little-endian RGB565, zlib-compressed.

func rgb565(r, g, b uint8) uint16 {
	return uint16(r>>3)<<11 | uint16(g>>2)<<5 | uint16(b>>3)
}

// encodeFrame expects a frameW x frameH image.
func encodeFrame(img *image.RGBA) ([]byte, error) {
	if img.Bounds().Dx() != frameW || img.Bounds().Dy() != frameH {
		return nil, errors.New("frame must be 800x368")
	}
	return encodeRGB565(img)
}

// encodeRGB565 packs an image of any size, rows top to bottom.
func encodeRGB565(img *image.RGBA) ([]byte, error) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	raw := make([]byte, 0, w*h*2)
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride:]
		for x := 0; x < w; x++ {
			raw = binary.LittleEndian.AppendUint16(raw, rgb565(row[x*4], row[x*4+1], row[x*4+2]))
		}
	}
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeFrame inflates a frame back to pixels (used by tests and tools).
func decodeFrame(z []byte) ([]uint16, error) {
	r, err := zlib.NewReader(bytes.NewReader(z))
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(raw) != frameW*frameH*2 {
		return nil, errors.New("bad frame size")
	}
	px := make([]uint16, len(raw)/2)
	for i := range px {
		px[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	return px, nil
}
