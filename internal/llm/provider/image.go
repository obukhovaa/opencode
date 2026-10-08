package provider

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"sync"

	// Registered for checkImage; png and jpeg register through the named
	// imports above.
	_ "image/gif"

	_ "golang.org/x/image/webp"
)

// imageFormatMIME maps the format names the registered decoders report to
// the media types every provider's image block accepts.
var imageFormatMIME = map[string]string{
	"png":  "image/png",
	"jpeg": "image/jpeg",
	"gif":  "image/gif",
	"webp": "image/webp",
}

// maxDecodedImagePixels bounds checkImage's full decode, which allocates
// the whole bitmap (up to 8 bytes per pixel for 16-bit PNG). Larger images
// get a header check only.
const maxDecodedImagePixels = 25_000_000

// imageCheckCacheSize caps the memo before it is cleared wholesale; entries
// are a hash and a short string, so the cap is about churn, not memory.
const imageCheckCacheSize = 1024

type imageCheckResult struct {
	mime string
	err  error
}

// imageChecks memoizes checkImage by content hash: attachments replay from
// session history on every request (and count_tokens call), and a full
// decode per turn would repeat the same work.
var imageChecks = struct {
	sync.Mutex
	m map[[sha256.Size]byte]imageCheckResult
}{m: map[[sha256.Size]byte]imageCheckResult{}}

// checkImage decodes image data and returns the media type of the format
// it actually holds. Providers reject undecodable image data — Kimi with a
// 400 "failed to decode image", Bedrock by resetting the HTTP/2 stream —
// and Anthropic 400s a declared media type that disagrees with the bytes
// (a JPEG labeled image/png). Attachments persist in session history, so
// one such image fails every later turn of its session. Converters send
// the returned type in place of the declared one, and replace the image
// with invalidImageNote when err is non-nil.
func checkImage(data []byte) (string, error) {
	key := sha256.Sum256(data)
	imageChecks.Lock()
	cached, ok := imageChecks.m[key]
	imageChecks.Unlock()
	if ok {
		return cached.mime, cached.err
	}

	mime, err := decodeImage(data)

	imageChecks.Lock()
	if len(imageChecks.m) >= imageCheckCacheSize {
		clear(imageChecks.m)
	}
	imageChecks.m[key] = imageCheckResult{mime: mime, err: err}
	imageChecks.Unlock()
	return mime, err
}

func decodeImage(data []byte) (string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if errors.Is(err, image.ErrFormat) {
		return "", errors.New("not a PNG, JPEG, GIF or WebP image")
	}
	if err != nil {
		return "", fmt.Errorf("corrupt image header: %w", err)
	}
	mime, ok := imageFormatMIME[format]
	if !ok {
		return "", fmt.Errorf("unsupported image format %q", format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return "", fmt.Errorf("invalid %s dimensions %dx%d", format, cfg.Width, cfg.Height)
	}
	// Header-only for oversized bitmaps and for animated WebP, which the
	// x/image decoder does not implement (it reports the format invalid).
	if cfg.Width*cfg.Height > maxDecodedImagePixels || isAnimatedWebP(data) {
		return mime, nil
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		// A feature the Go decoder lacks (arithmetic-coded JPEG, ...) is
		// not corruption; trust the header the providers will also read.
		var jpegUnsupported jpeg.UnsupportedError
		var pngUnsupported png.UnsupportedError
		if errors.As(err, &jpegUnsupported) || errors.As(err, &pngUnsupported) {
			return mime, nil
		}
		return "", fmt.Errorf("corrupt %s data: %w", format, err)
	}
	return mime, nil
}

// isAnimatedWebP reports whether data is an extended-format WebP with the
// animation flag set: "RIFF" size "WEBP", then a "VP8X" chunk whose first
// payload byte carries the flag in bit 1.
func isAnimatedWebP(data []byte) bool {
	return len(data) > 20 &&
		string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" &&
		string(data[12:16]) == "VP8X" && data[20]&0x02 != 0
}
