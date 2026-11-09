// Command icongen draws resources/icon.png, the 1024x1024 app icon, with
// the standard library alone.
//
//	go run ./tools/icongen
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
)

const size = 1024

func main() {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	// 3x3 supersampling gives smooth arcs without a drawing library.
	const ss = 3
	for y := range size {
		for x := range size {
			var r, g, b, a float64
			for sy := range ss {
				for sx := range ss {
					px := float64(x) + (float64(sx)+0.5)/ss
					py := float64(y) + (float64(sy)+0.5)/ss
					cr, cg, cb, ca := sample(px, py)
					r += cr
					g += cg
					b += cb
					a += ca
				}
			}
			n := float64(ss * ss)
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(clamp(r/n, 0, 255)),
				G: uint8(clamp(g/n, 0, 255)),
				B: uint8(clamp(b/n, 0, 255)),
				A: uint8(clamp(a/n, 0, 255)),
			})
		}
	}
	if err := os.MkdirAll("resources", 0o755); err != nil {
		log.Fatal(err)
	}
	f, err := os.Create("resources/icon.png")
	if err != nil {
		log.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}

// sample returns the straight RGBA of one point, with the blue rounded
// square, a white gauge arc, a needle and a center dot.
func sample(x, y float64) (float64, float64, float64, float64) {
	const (
		cx     = size / 2.0
		cy     = 560.0
		radius = 300.0
		stroke = 64.0
	)
	// Rounded square background.
	const margin = 40.0
	const corner = 210.0
	if !inRoundedRect(x, y, margin, margin, size-margin, size-margin, corner) {
		return 0, 0, 0, 0
	}
	// Diagonal blue gradient.
	t := (x + y) / (2 * size)
	bgR := lerp(29, 14, t)
	bgG := lerp(78, 165, t)
	bgB := lerp(216, 233, t)
	r, g, b := bgR, bgG, bgB
	alpha := 255.0

	dx, dy := x-cx, y-cy
	dist := math.Hypot(dx, dy)
	angle := math.Atan2(dy, dx) // -pi..pi, 0 is right
	// Gauge arc: the top half, from 180° to 360°, drawn in white.
	if angle >= math.Pi-0.12 || angle <= -0.12 {
		if math.Abs(dist-radius) <= stroke/2 {
			r, g, b = 255, 255, 255
		}
	}
	// Needle pointing up-right, with rounded ends.
	needleAngle := -0.95
	nx := cx + math.Cos(needleAngle)*radius*0.72
	ny := cy + math.Sin(needleAngle)*radius*0.72
	if distToSegment(x, y, cx, cy+18, nx, ny) <= 17 {
		r, g, b = 255, 255, 255
	}
	// Center dot.
	if math.Hypot(x-cx, y-(cy+18)) <= 38 {
		r, g, b = 255, 255, 255
	}
	return r, g, b, alpha
}

func inRoundedRect(x, y, left, top, right, bottom, radius float64) bool {
	if x < left || x > right || y < top || y > bottom {
		return false
	}
	// Clamp to the inner rectangle; the distance to it is zero inside.
	qx := math.Max(left+radius-x, 0)
	qx = math.Max(qx, x-(right-radius))
	qy := math.Max(top+radius-y, 0)
	qy = math.Max(qy, y-(bottom-radius))
	if qx == 0 || qy == 0 {
		return true
	}
	return math.Hypot(qx, qy) <= radius
}

func distToSegment(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	if dx == 0 && dy == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	t := ((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
