package filerefs

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registers the WebP decoder with image.Decode
)

// Image limits. MaxImageSize is what we SEND (the strictest common
// provider cap; a rejected image fails the whole request and would then
// sit in session history). Larger sources up to MaxImageSourceSize are
// shrunk to fit instead of being refused.
const (
	MaxImageSize       = 5 * 1024 * 1024  // per image, as sent
	MaxImageSourceSize = 20 * 1024 * 1024 // largest source file we will read
	MaxTotalImageSize  = 20 * 1024 * 1024 // per message, as sent
	MaxImagesPerTurn   = 10

	// maxImageDim is the longest side providers accept (Anthropic: 8000 px).
	maxImageDim = 8000
	// maxDecodePixels bounds decode memory (≈4 bytes/pixel): a 20 MiB PNG
	// can describe a gigapixel canvas.
	maxDecodePixels = 64_000_000
	// shrinkStartDim is where shrinking begins; providers downscale
	// anything past ~1.5k px anyway, so detail above this is wasted.
	shrinkStartDim = 2048
	shrinkMinDim   = 256
	jpegQuality    = 85
)

// needsShrink reports whether data (already within the source cap) must
// be resized before sending: too many bytes, or a side past the provider
// maximum. It reads only the image header.
func needsShrink(data []byte) (bool, error) {
	if len(data) <= MaxImageSize {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return false, nil // unknown/odd header: send as-is, provider decides
		}
		return cfg.Width > maxImageDim || cfg.Height > maxImageDim, nil
	}
	return true, nil
}

// shrinkImage decodes data, scales it down and re-encodes it as JPEG
// until it fits MaxImageSize and maxImageDim. Alpha is flattened over
// white; an animated GIF keeps its first frame. It returns the new bytes,
// their media type, and a short note for the user.
func shrinkImage(data []byte) ([]byte, string, string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", "", fmt.Errorf("image is larger than %d MiB and could not be read to resize it: %v", MaxImageSize>>20, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxDecodePixels {
		return nil, "", "", fmt.Errorf("image is %dx%d pixels; too large to resize here (resize it yourself)", cfg.Width, cfg.Height)
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", "", fmt.Errorf("could not decode %s to resize it: %v", format, err)
	}

	b := src.Bounds()
	dim := max(b.Dx(), b.Dy())
	side := min(dim, shrinkStartDim)
	for {
		scaled := scaleToSide(src, side)
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: jpegQuality}); err != nil {
			return nil, "", "", err
		}
		if buf.Len() <= MaxImageSize {
			note := fmt.Sprintf("resized from %s (%dx%d) to %s (%dx%d)",
				humanBytes(len(data)), b.Dx(), b.Dy(), humanBytes(buf.Len()), scaled.Bounds().Dx(), scaled.Bounds().Dy())
			return buf.Bytes(), "image/jpeg", note, nil
		}
		// Try the starting size once, then keep shrinking by a quarter
		// until the floor.
		next := side * 3 / 4
		if next < shrinkMinDim {
			return nil, "", "", errors.New("image could not be shrunk under the size limit")
		}
		side = next
	}
}

// scaleToSide resizes src so its longest side is at most side pixels
// (never upscales) and composites it over white so JPEG has no alpha.
func scaleToSide(src image.Image, side int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if m := max(w, h); m > side {
		w, h = max(w*side/m, 1), max(h*side/m, 1)
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

// Compile-time use of the decoders image.Decode needs for these formats.
var (
	_ = gif.Decode
	_ = png.Decode
)
