package cli

import (
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rceman/game-forge/internal/visual"
)

// pixelSample is one sampled coordinate.
type pixelSample struct {
	X     int    `json:"x"`
	Y     int    `json:"y"`
	R     uint32 `json:"r"`
	G     uint32 `json:"g"`
	B     uint32 `json:"b"`
	A     uint32 `json:"a"`
	Color string `json:"color"`
}

// cmdPixel samples real rendered pixels.
//
// The project supplies the case/region semantics; Game Forge owns the capture,
// the image decode, coordinate validation and the result. No external image
// tooling is involved.
func cmdPixel(args []string) int {
	fs := newFlagSet("pixel")
	caseName := fs.String("case", "", "visual case to capture (defaults to the project's)")
	ticks := fs.Int("ticks", -1, "ticks before capture")
	seed := fs.String("seed", "", "seed preset or integer")
	world := fs.String("world", "", "world seed preset or integer")
	region := fs.String("region", "full", "named capture region")
	imagePath := fs.String("image", "", "sample an existing image instead of capturing")
	out := fs.String("out", "", "keep the capture at this path (default: a temporary file)")
	jsonOut := fs.Bool("json", false, "emit JSON")
	timeout := fs.Duration("timeout", 5*time.Minute, "overall timeout")
	if code, done := fs.parse(args); done {
		return code
	}

	points, err := parsePoints(fs.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge pixel: %v\n", err)
		return ExitUsage
	}
	if len(points) == 0 {
		fmt.Fprintln(os.Stderr, "game-forge pixel: at least one x,y coordinate is required")
		return ExitUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	path := *imagePath
	cleanup := func() {}
	if path == "" {
		h, err := newHarness()
		if err != nil {
			fmt.Fprintf(os.Stderr, "game-forge pixel: %v\n", err)
			return ExitFail
		}
		if *caseName == "" {
			*caseName = h.m.Visual.DefaultCase
		}
		if *ticks < 0 {
			*ticks = h.m.Visual.DefaultTicks
		}
		dir := *out
		if dir == "" {
			tmp, err := os.MkdirTemp("", "game-forge-pixel-*")
			if err != nil {
				fmt.Fprintf(os.Stderr, "game-forge pixel: %v\n", err)
				return ExitFail
			}
			cleanup = func() { _ = os.RemoveAll(tmp) }
			dir = filepath.Join(tmp, "sample.png")
		}
		res, err := captureForPixel(ctx, h, *caseName, *ticks, *seed, *world, *region, dir)
		if err != nil {
			cleanup()
			fmt.Fprintf(os.Stderr, "game-forge pixel: %v\n", err)
			return ExitFail
		}
		path = res
	}
	defer cleanup()

	samples, w, hgt, err := sampleImage(path, points)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge pixel: %v\n", err)
		return ExitFail
	}

	if *jsonOut {
		return emitJSON(map[string]any{
			"image":   path,
			"width":   w,
			"height":  hgt,
			"samples": samples,
		})
	}
	fmt.Printf("image=%s size=%dx%d\n", path, w, hgt)
	for _, s := range samples {
		fmt.Printf("(%d,%d) = (%d, %d, %d)\n", s.X, s.Y, s.R, s.G, s.B)
	}
	return ExitOK
}

// captureForPixel captures one visual case to path and returns the path.
func captureForPixel(ctx context.Context, h *harness, caseName string, ticks int, seed, world, region, path string) (string, error) {
	srv, err := h.ensureServer(ctx, "dev", 0)
	if err != nil {
		return "", err
	}
	defer h.stopOwnedServers()
	sess, cleanup, err := h.openBrowser(ctx, srv.URL())
	if err != nil {
		return "", err
	}
	defer cleanup()
	_, err = visual.Shot(ctx, sess, visual.ShotOptions{
		Case:      caseName,
		Ticks:     ticks,
		Seed:      seed,
		WorldSeed: world,
		Region:    region,
		Output:    path,
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// parsePoints parses x,y arguments.
func parsePoints(args []string) ([][2]int, error) {
	var out [][2]int
	for _, a := range args {
		xs, ys, ok := strings.Cut(a, ",")
		if !ok {
			return nil, fmt.Errorf("coordinate %q must be written x,y", a)
		}
		x, err := strconv.Atoi(strings.TrimSpace(xs))
		if err != nil {
			return nil, fmt.Errorf("coordinate %q: %v", a, err)
		}
		y, err := strconv.Atoi(strings.TrimSpace(ys))
		if err != nil {
			return nil, fmt.Errorf("coordinate %q: %v", a, err)
		}
		out = append(out, [2]int{x, y})
	}
	return out, nil
}

// sampleImage decodes path and reads the requested points, validating that
// every coordinate is inside the image.
func sampleImage(path string, points [][2]int) ([]pixelSample, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode %s: %w", path, err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	samples := make([]pixelSample, 0, len(points))
	for _, p := range points {
		if p[0] < 0 || p[1] < 0 || p[0] >= w || p[1] >= h {
			return nil, w, h, fmt.Errorf("coordinate (%d,%d) is outside the %dx%d image", p[0], p[1], w, h)
		}
		r, g, bl, a := img.At(b.Min.X+p[0], b.Min.Y+p[1]).RGBA()
		samples = append(samples, pixelSample{
			X: p[0], Y: p[1],
			R: r >> 8, G: g >> 8, B: bl >> 8, A: a >> 8,
			Color: fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, bl>>8),
		})
	}
	return samples, w, h, nil
}
