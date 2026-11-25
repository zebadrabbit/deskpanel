package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Radar collector: IEM N0Q composites stitched over an OpenStreetMap base map darkened here.

const (
	radarFrames = 5
	radarStep   = 300 // seconds between frames
)

type tileClient struct {
	hc *http.Client
	ua string
}

// get fetches and decodes one tile.
func (t tileClient) get(ctx context.Context, url string) (image.Image, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", t.ua)
	resp, err := t.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	img, _, err := image.Decode(io.LimitReader(resp.Body, 4<<20))
	return img, err
}

// stitch lays every tile of the view into one frameW x frameH image.
// urlFmt takes z, x, y.
func stitch(ctx context.Context, t tileClient, v view, urlFmt string) (*image.RGBA, error) {
	out := image.NewRGBA(image.Rect(0, 0, frameW, frameH))
	for _, tl := range v.tiles() {
		img, err := t.get(ctx, fmt.Sprintf(urlFmt, v.z, tl[0], tl[1]))
		if err != nil {
			return nil, err
		}
		ox, oy := v.tileOrigin(tl[0], tl[1])
		draw.Draw(out, image.Rect(ox, oy, ox+256, oy+256), img, img.Bounds().Min, draw.Src)
	}
	return out, nil
}

type radar struct {
	v            view
	t            tileClient
	iem, basemap string
	cacheDir     string
	base         *image.RGBA
	mu           sync.Mutex
	frames       map[int64][]byte
}

func newRadar(v view, t tileClient, iem, basemap, cacheDir string) *radar {
	return &radar{v: v, t: t, iem: iem, basemap: basemap, cacheDir: cacheDir, frames: map[int64][]byte{}}
}

func (r *radar) Section() string         { return "weather" }
func (r *radar) Key() string             { return "frames" }
func (r *radar) Interval() time.Duration { return 150 * time.Second }

// frame returns the encoded frame for a valid time.
func (r *radar) frame(t int64) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.frames[t]
	return b, ok
}

func (r *radar) valid(ctx context.Context) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.iem+"/data/gis/images/4326/USCOMP/n0q_0.json", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", r.t.ua)
	resp, err := r.t.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("n0q_0.json: %s", resp.Status)
	}
	var j struct {
		Meta struct {
			Valid time.Time `json:"valid"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&j); err != nil {
		return 0, err
	}
	if j.Meta.Valid.IsZero() {
		return 0, fmt.Errorf("n0q_0.json: no valid time")
	}
	return j.Meta.Valid.Unix(), nil
}

// ensureBase builds the base map once, using the disk cache when configured.
func (r *radar) ensureBase(ctx context.Context) error {
	if r.base != nil {
		return nil
	}
	var path string
	if r.cacheDir != "" {
		path = filepath.Join(r.cacheDir, fmt.Sprintf("osmdark-%d-%d-%d.png", r.v.z, int(r.v.x0), int(r.v.y0)))
		if f, err := os.Open(path); err == nil {
			img, err := png.Decode(f)
			f.Close()
			if err == nil && img.Bounds().Dx() == frameW && img.Bounds().Dy() == frameH {
				b := image.NewRGBA(img.Bounds())
				draw.Draw(b, b.Bounds(), img, img.Bounds().Min, draw.Src)
				r.base = b
				return nil
			}
		}
	}
	b, err := stitch(ctx, r.t, r.v, r.basemap+"/%d/%d/%d.png")
	if err != nil {
		return err
	}
	darkenBase(b)
	r.base = b
	if path != "" {
		// ponytail: cache write failure is ignored, the map just refetches next start
		if os.MkdirAll(r.cacheDir, 0o755) == nil {
			if f, err := os.Create(path); err == nil {
				png.Encode(f, b)
				f.Close()
			}
		}
	}
	return nil
}

// darkenBase applies dark to every pixel (once, before caching).
func darkenBase(img *image.RGBA) {
	for i := 0; i < len(img.Pix); i += 4 {
		c := dark(color.RGBA{img.Pix[i], img.Pix[i+1], img.Pix[i+2], 255})
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 255
	}
}

// dark: invert lightness, keep hue, mute saturation to 35 %, slight blue lift.
func dark(c color.RGBA) color.RGBA {
	r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
	l := (math.Max(r, math.Max(g, b)) + math.Min(r, math.Min(g, b))) / 2
	nl := (1-l)*0.55 + 0.035
	f := func(v float64) uint8 { return uint8(math.Max(0, math.Min(1, nl+(v-l)*0.35)) * 255) }
	o := color.RGBA{f(r), f(g), f(b), 255}
	o.B = uint8(math.Min(255, float64(o.B)*1.12+3))
	return o
}

// build renders any of the five wanted frames for valid that we don't hold and
// returns the times it built (so a caller can discard them if the scan moved).
func (r *radar) build(ctx context.Context, valid int64) (built []int64, err error) {
	for k := 0; k < radarFrames; k++ {
		ts := valid - int64(k*radarStep)
		if _, ok := r.frame(ts); ok {
			continue
		}
		layer := "nexrad-n0q-900913"
		if k > 0 {
			layer = fmt.Sprintf("nexrad-n0q-900913-m%02dm", k*5)
		}
		rad, err := stitch(ctx, r.t, r.v, r.iem+"/cache/tile.py/1.0.0/"+layer+"/%d/%d/%d.png")
		if err != nil {
			return built, err
		}
		img := image.NewRGBA(r.base.Bounds())
		copy(img.Pix, r.base.Pix)
		draw.DrawMask(img, img.Bounds(), rad, image.Point{}, image.NewUniform(color.Alpha{217}), image.Point{}, draw.Over) // 85 %
		z, err := encodeFrame(img)
		if err != nil {
			return built, err
		}
		r.mu.Lock()
		r.frames[ts] = z
		r.mu.Unlock()
		built = append(built, ts)
	}
	return built, nil
}

func (r *radar) drop(times []int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ts := range times {
		delete(r.frames, ts)
	}
}

// Collect returns the valid times (oldest first) of the frames now available.
func (r *radar) Collect(ctx context.Context) (any, error) {
	valid, err := r.valid(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.ensureBase(ctx); err != nil {
		return nil, err
	}
	// honest labels: tiles fetched while a new scan lands mix scans (and the
	// -mNNm layers shift), so drop what this pass built and redo it once
	for i := 0; ; i++ {
		built, err := r.build(ctx, valid)
		if err != nil {
			r.drop(built)
			return nil, err
		}
		nv, err := r.valid(ctx)
		if err != nil || nv == valid {
			break
		}
		r.drop(built)
		if i == 1 {
			return nil, fmt.Errorf("radar scan moved twice while building")
		}
		valid = nv
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	times := make([]int64, 0, radarFrames)
	for k := radarFrames - 1; k >= 0; k-- {
		times = append(times, valid-int64(k*radarStep))
	}
	for ts := range r.frames {
		if ts > valid || ts <= valid-int64(radarFrames*radarStep) || (valid-ts)%radarStep != 0 {
			delete(r.frames, ts)
		}
	}
	return times, nil
}
