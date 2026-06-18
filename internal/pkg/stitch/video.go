package stitch

import (
	"fmt"
	"image"
	"image/draw"
	"io"
	"os"
	"time"

	"github.com/klauspost/compress/zstd"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// Frames are buffered in RAM zstd-compressed (lossless) during a sequence, so a
// long train does not need every full RGBA frame resident at once. They are
// decoded again at the end to assemble the panorama and encode the video.
var (
	zstdEnc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	zstdDec, _ = zstd.NewReader(nil)
)

// encodeFrame returns a zstd-compressed, origin-based, tightly-packed RGBA copy
// of img, plus the (origin-based) rectangle needed to decode it again.
func encodeFrame(img image.Image) ([]byte, image.Rectangle) {
	b := img.Bounds()
	rect := image.Rect(0, 0, b.Dx(), b.Dy())

	rgba, ok := img.(*image.RGBA)
	if !ok || rgba.Rect != rect || rgba.Stride != rect.Dx()*4 {
		// Normalise to a tight, origin-based buffer.
		dst := image.NewRGBA(rect)
		draw.Draw(dst, rect, img, b.Min, draw.Src)
		rgba = dst
	}

	return zstdEnc.EncodeAll(rgba.Pix, nil), rect
}

// decodeFrame reverses encodeFrame.
func decodeFrame(blob []byte, rect image.Rectangle) (*image.RGBA, error) {
	pix, err := zstdDec.DecodeAll(blob, make([]byte, 0, rect.Dx()*rect.Dy()*4))
	if err != nil {
		return nil, err
	}
	return &image.RGBA{Pix: pix, Stride: rect.Dx() * 4, Rect: rect}, nil
}

// makeVideo encodes the frames into an H.264 MP4 and returns the file bytes.
// Playback speed matches the real sequence timing (average fps over its span).
func makeVideo(frames []*image.RGBA, ts []time.Time, startTS time.Time) ([]byte, error) {
	if len(frames) == 0 {
		return nil, fmt.Errorf("no frames to encode")
	}

	dur := ts[len(ts)-1].Sub(startTS).Seconds()
	fps := 25.0
	if dur > 0 {
		fps = float64(len(frames)) / dur
	}
	if fps < 1 {
		fps = 1
	}
	if fps > 60 {
		fps = 60
	}

	b := frames[0].Bounds()
	tmp, err := os.CreateTemp("", "trainbot-*.mp4")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)

	pr, pw := io.Pipe()
	go func() {
		var werr error
		for _, f := range frames {
			if _, werr = pw.Write(f.Pix); werr != nil {
				break
			}
		}
		pw.CloseWithError(werr)
	}()

	err = ffmpeg.Input("pipe:",
		ffmpeg.KwArgs{
			"format":    "rawvideo",
			"pix_fmt":   "rgba",
			"s":         fmt.Sprintf("%dx%d", b.Dx(), b.Dy()),
			"framerate": fmt.Sprintf("%f", fps),
		}).
		Output(tmpName,
			ffmpeg.KwArgs{
				"c:v":      "libx264",
				"preset":   "veryfast",
				"crf":      "20",
				"pix_fmt":  "yuv420p",
				// yuv420p needs even dimensions; pad up if the crop is odd.
				"vf":       "pad=ceil(iw/2)*2:ceil(ih/2)*2",
				"movflags": "+faststart",
			}).
		WithInput(pr).
		OverWriteOutput().
		Run()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg encode failed: %w", err)
	}

	return os.ReadFile(tmpName)
}
