package stitch

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	"math"
	"time"

	"github.com/rs/zerolog/log"
	"jo-m.ch/go/trainbot/internal/pkg/prometheus"
)

const (
	maxMemoryMB = 1024 * 1024 * 50

	// Width (px) of the linear cross-fade between adjacent strips. Clamped per
	// seam to the involved strip widths, so narrow strips just fade less.
	featherPx = 8
)

// featherMask builds a width×height alpha mask that is fully opaque except for a
// linear ramp over `feather` columns on one side. Drawing a strip through this
// mask with draw.Over cross-fades it into the (already drawn, opaque) neighbour.
func featherMask(width, height, feather int, rampLeft bool) *image.Alpha {
	m := image.NewAlpha(image.Rect(0, 0, width, height))
	// The mask only varies along x, so compute one row and replicate it.
	row := make([]uint8, width)
	for x := range row {
		a := 255
		if rampLeft && x < feather {
			a = x * 255 / feather
		} else if !rampLeft && x >= width-feather {
			a = (width - 1 - x) * 255 / feather
		}
		row[x] = uint8(a)
	}
	for y := 0; y < height; y++ {
		copy(m.Pix[y*m.Stride:], row)
	}
	return m
}

func isign(x int) int {
	if x > 0 {
		return 1
	}
	if x < 0 {
		return -1
	}
	return 0
}

func sign(x float64) float64 {
	if x > 0 {
		return 1
	}
	if x < 0 {
		return -1
	}
	return 0
}

func stitch(frames []image.Image, dx []int) (*image.RGBA, error) {
	t0 := time.Now()
	defer func() {
		log.Trace().Dur("dur", time.Since(t0)).Msg("stitch() duration")
	}()

	log.Info().Ints("dx", dx).Int("len(frames)", len(frames)).Msg("stitch()")

	// Sanity checks.
	if len(dx) < 2 {
		return nil, errors.New("sequence too short to stitch")
	}
	if len(frames) != len(dx) {
		log.Panic().Msg("frames and dx do not have the same length, this should not happen")
	}
	fb := frames[0].Bounds()
	for _, f := range frames {
		if f.Bounds() != fb {
			log.Panic().Msg("frame bounds or size not consistent, this should not happen")
		}
	}

	// All dx must have a consistent sign; that sign is the direction of travel.
	sign := isign(dx[0])
	for _, x := range dx[1:] {
		if isign(x) != sign {
			return nil, errors.New("dx elements do not have consistent sign")
		}
	}

	W := fb.Dx()
	h := fb.Dy()
	n := len(frames)

	// Each frame contributes a vertical strip of width |dx[i]| to the panorama.
	// We sample that strip from the centre of the frame, because findOffset()
	// measures the alignment on a centred crop and because lens distortion is
	// smallest there. (The previous implementation kept the leading-edge strip
	// of every frame, i.e. the most distorted, least-well-registered columns.)
	//
	// The centre strips reconstruct the body of the train. The leading/trailing
	// tip of the train is only ever seen at a frame edge, so the first and last
	// frames are extended outwards to their frame edge to form the end caps.
	//
	// Output width: the outer half of the first frame (cap), plus the sum of all
	// strips (body), plus the outer half of the last frame (cap).
	a0 := iabs(dx[0])
	aLast := iabs(dx[n-1])
	cap0 := (W - a0) / 2 // Outer-half cap width taken from the first frame.
	w := cap0 + (W-aLast)/2
	for _, x := range dx {
		w += iabs(x)
	}

	// Memory alloc sanity check.
	rect := image.Rect(0, 0, w, h)
	if rect.Size().X*rect.Size().Y*4 > maxMemoryMB {
		return nil, fmt.Errorf("would allocate too much memory: size %dx%d", rect.Size().X, rect.Size().Y)
	}
	img := image.NewRGBA(rect)

	pos := 0 // Prefix sum of |dx| over the frames already placed.
	for i, f := range frames {
		a := iabs(dx[i])

		// Centred source strip, and its placement in the output.
		srcLo := (W - a) / 2
		var destLo, destHi int
		if sign > 0 {
			// Assemble left to right.
			destLo = cap0 + pos
			destHi = destLo + a
			if i == 0 {
				// Left cap: extend to the left frame edge.
				destLo = 0
				srcLo = 0
			}
			if i == n-1 {
				// Right cap: extend to the right frame edge.
				destHi = w
			}
		} else {
			// Assemble right to left (mirrored).
			destHi = w - (cap0 + pos)
			destLo = destHi - a
			if i == 0 {
				// Right cap: extend to the right frame edge.
				destHi = w
			}
			if i == n-1 {
				// Left cap: extend to the left frame edge.
				destLo = 0
				srcLo = 0
			}
		}

		// Feather this strip into the previously drawn neighbour to hide the
		// seam. Frame 0 is drawn first and has no neighbour yet, so it stays
		// hard; every later frame fades in over the side facing frame 0 (the
		// left for forward assembly, the right for backward).
		feather := 0
		if i > 0 {
			feather = featherPx
			if feather > a {
				feather = a
			}
			if prev := iabs(dx[i-1]); feather > prev {
				feather = prev
			}
		}

		switch {
		case feather <= 0:
			dest := image.Rect(destLo, 0, destHi, h)
			src := fb.Min.Add(image.Pt(srcLo, 0))
			draw.Draw(img, dest, f, src, draw.Src)
		case sign > 0:
			// Extend left into the neighbour, ramping up from the left.
			dest := image.Rect(destLo-feather, 0, destHi, h)
			src := fb.Min.Add(image.Pt(srcLo-feather, 0))
			mask := featherMask(dest.Dx(), h, feather, true)
			draw.DrawMask(img, dest, f, src, mask, image.Point{}, draw.Over)
		default:
			// Extend right into the neighbour, ramping down to the right.
			dest := image.Rect(destLo, 0, destHi+feather, h)
			src := fb.Min.Add(image.Pt(srcLo, 0))
			mask := featherMask(dest.Dx(), h, feather, false)
			draw.DrawMask(img, dest, f, src, mask, image.Point{}, draw.Over)
		}

		pos += a
	}

	return img, nil
}

// Train represents a detected train.
type Train struct {
	StartTS time.Time

	// Always positive.
	NFrames int

	// Always positive (absolute value).
	LengthPx float64
	// Positive sign means movement to the right, negative to the left.
	SpeedPxS float64
	// Positive sign means increasing speed for trains going to the right, breaking for trains going to the left.
	AccelPxS2 float64

	Conf Config

	Image *image.RGBA `json:"-"`
	// Video is the H.264/MP4 animation of the passing train.
	Video []byte `json:"-"`
}

// LengthM returns the absolute length in m.
func (t *Train) LengthM() float64 {
	return math.Abs(t.LengthPx) / t.Conf.PixelsPerM
}

// SpeedMpS returns the absolute speed in m/s.
func (t *Train) SpeedMpS() float64 {
	return math.Abs(t.SpeedPxS) / t.Conf.PixelsPerM
}

// AccelMpS2 returns the acceleration in m/2^2, corrected for speed direction:
// Positive means accelerating, negative means breaking.
func (t *Train) AccelMpS2() float64 {
	return t.AccelPxS2 / t.Conf.PixelsPerM * sign(t.SpeedPxS)
}

// Direction returns the train direction. Right = true, left = false.
func (t *Train) Direction() bool {
	return t.SpeedPxS > 0
}

// DirectionS returns the train direction as string "left" or "right".
func (t *Train) DirectionS() string {
	if t.SpeedPxS > 0 {
		return "right"
	}

	return "left"
}

// fitAndStitch tries to stitch an image from a sequence.
// Will first try to fit a constant acceleration speed model for smoothing.
// Might modify seq (drops leading frames with no movement).
func fitAndStitch(seq sequence, c Config) (*Train, error) {
	start := time.Now()
	defer func() {
		log.Trace().Dur("dur", time.Since(start)).Msg("fitAndStitch() duration")
	}()

	log.Info().Ints("dx", seq.dx).Int("len(frames)", len(seq.frames)).Msg("fitAndStitch()")

	// Sanity checks.
	if len(seq.frames) != len(seq.dx) || len(seq.frames) != len(seq.ts) {
		log.Panic().Msg("length of frames, dx, ts are not equal, this should not happen")
	}
	if seq.startTS == nil {
		log.Panic().Msg("startTS is nil, this should not happen")
	}
	if len(seq.dx) == 0 || seq.dx[0] == 0 {
		log.Panic().Int("len", len(seq.dx)).Msg("sequence is empty or first value is 0")
	}

	// Remove trailing zeros.
	for len(seq.dx) > 0 && seq.dx[len(seq.dx)-1] == 0 {
		seq.dx = seq.dx[:len(seq.dx)-1]
		seq.ts = seq.ts[:len(seq.ts)-1]
		seq.frames = seq.frames[:len(seq.frames)-1]
	}
	prometheus.RecordSequenceLength(len(seq.frames))

	dxFit, ds, v0, a, err := fitDx(seq, float64(c.maxPxPerFrame(1)))
	if err != nil {
		prometheus.RecordFitAndStitchResult("unable_to_fit")
		return nil, fmt.Errorf("was not able to fit the sequence: %w", err)
	}

	if math.Abs(ds) < c.minLengthPx() {
		prometheus.RecordFitAndStitchResult("too_short")
		return nil, fmt.Errorf("discarded because too short, %f < %f", ds, c.minLengthPx())
	}

	// Estimate speed at halftime.
	t0 := seq.ts[0]
	tMid := seq.ts[len(seq.ts)/2]
	speed := v0 + a*tMid.Sub(t0).Seconds()

	if math.Abs(speed) < c.minSpeedPxPS() {
		prometheus.RecordFitAndStitchResult("too_slow")
		return nil, fmt.Errorf("discarded because too slow, %f < %f", speed, c.minSpeedPxPS())
	}

	// Decode the buffered (zstd-compressed) frames for assembly.
	frames := make([]image.Image, len(seq.frames))
	decoded := make([]*image.RGBA, len(seq.frames))
	for i, blob := range seq.frames {
		f, err := decodeFrame(blob, seq.frameRect)
		if err != nil {
			prometheus.RecordFitAndStitchResult("unable_to_assemble_image")
			return nil, fmt.Errorf("unable to decode frame: %w", err)
		}
		frames[i] = f
		decoded[i] = f
	}

	img, err := stitch(frames, dxFit)
	if err != nil {
		prometheus.RecordFitAndStitchResult("unable_to_assemble_image")
		return nil, fmt.Errorf("unable to assemble image: %w", err)
	}

	// The video is a nice-to-have; a failure to encode must not lose the train.
	video, err := makeVideo(decoded, seq.ts, *seq.startTS)
	if err != nil {
		log.Err(err).Msg("unable to encode video, continuing without it")
		video = nil
	}

	prometheus.RecordFitAndStitchResult("success")
	return &Train{
		t0,
		len(seq.frames),
		ds,
		-speed, // Negate because when things move to the left we get positive dx values.
		-a,
		c,
		img,
		video,
	}, nil
}
