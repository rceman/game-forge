package visual

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
)

// cropPNG writes the rect region of src into dst.
func cropPNG(src, dst string, rect RegionRect) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open screenshot: %w", err)
	}
	defer in.Close()
	img, err := png.Decode(in)
	if err != nil {
		return fmt.Errorf("decode screenshot: %w", err)
	}
	bounds := img.Bounds()
	r := image.Rect(rect.X, rect.Y, rect.X+rect.Width, rect.Y+rect.Height).Intersect(bounds)
	if r.Empty() {
		return fmt.Errorf("region %+v is empty within %v", rect, bounds)
	}
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(out, out.Bounds(), img, r.Min, draw.Src)
	f, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create cropped screenshot: %w", err)
	}
	defer f.Close()
	if err := png.Encode(f, out); err != nil {
		return fmt.Errorf("encode cropped screenshot: %w", err)
	}
	return nil
}
