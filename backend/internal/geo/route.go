// Package geo réduit une trace GPS à la silhouette qu'affichent les listes de
// courses, au format « polyline » de Google — celui du summary_polyline de
// Strava, pour que l'app ne lise qu'un seul format quelle que soit la source.
package geo

import (
	"math"
	"strings"
)

// Point : coordonnée WGS84 en degrés.
type Point struct {
	Lat float64
	Lng float64
}

// maxRoutePoints borne la silhouette : au-delà, la vignette n'affiche rien de
// plus et le flux s'alourdit pour rien.
const maxRoutePoints = 300

// RoutePolyline simplifie une trace et l'encode. Chaîne vide quand il n'y a
// pas de tracé à montrer (tapis, GPS absent).
func RoutePolyline(pts []Point) string {
	clean := make([]Point, 0, len(pts))
	for _, p := range pts {
		if validPoint(p) {
			clean = append(clean, p)
		}
	}
	if len(clean) < 2 {
		return ""
	}
	return EncodePolyline(Simplify(clean, maxRoutePoints))
}

func validPoint(p Point) bool {
	if math.IsNaN(p.Lat) || math.IsNaN(p.Lng) {
		return false
	}
	if p.Lat < -90 || p.Lat > 90 || p.Lng < -180 || p.Lng > 180 {
		return false
	}
	// (0, 0) : un capteur sans fix, pas un passage dans le golfe de Guinée.
	return p.Lat != 0 || p.Lng != 0
}

// Simplify applique Ramer-Douglas-Peucker en mètres. La tolérance part d'une
// fraction de l'emprise (un demi-pixel sur une vignette) et double tant que la
// trace dépasse maxPoints.
func Simplify(pts []Point, maxPoints int) []Point {
	if len(pts) <= 2 {
		return pts
	}
	xy := project(pts)
	minX, maxX, minY, maxY := xy[0][0], xy[0][0], xy[0][1], xy[0][1]
	for _, v := range xy {
		minX = math.Min(minX, v[0])
		maxX = math.Max(maxX, v[0])
		minY = math.Min(minY, v[1])
		maxY = math.Max(maxY, v[1])
	}
	eps := math.Max(2, math.Hypot(maxX-minX, maxY-minY)/500)
	for {
		keep := rdp(xy, eps)
		if len(keep) <= maxPoints || maxPoints < 2 {
			out := make([]Point, len(keep))
			for i, k := range keep {
				out[i] = pts[k]
			}
			return out
		}
		eps *= 2
	}
}

// project passe en plan local (équirectangulaire), assez juste à l'échelle d'une sortie.
func project(pts []Point) [][2]float64 {
	k := math.Cos(pts[0].Lat * math.Pi / 180)
	out := make([][2]float64, len(pts))
	for i, p := range pts {
		out[i] = [2]float64{p.Lng * k * 111320, p.Lat * 110540}
	}
	return out
}

// rdp renvoie les indices conservés, dans l'ordre. Itératif : une trace de
// plusieurs milliers de points ne doit pas creuser la pile.
func rdp(xy [][2]float64, eps float64) []int {
	n := len(xy)
	keep := make([]bool, n)
	keep[0], keep[n-1] = true, true
	stack := [][2]int{{0, n - 1}}
	for len(stack) > 0 {
		seg := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		a, b := seg[0], seg[1]
		best, at := 0.0, -1
		for i := a + 1; i < b; i++ {
			if d := segmentDistance(xy[i], xy[a], xy[b]); d > best {
				best, at = d, i
			}
		}
		if at >= 0 && best > eps {
			keep[at] = true
			stack = append(stack, [2]int{a, at}, [2]int{at, b})
		}
	}
	idx := make([]int, 0, n)
	for i, k := range keep {
		if k {
			idx = append(idx, i)
		}
	}
	return idx
}

func segmentDistance(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	if dx == 0 && dy == 0 {
		return math.Hypot(p[0]-a[0], p[1]-a[1])
	}
	t := ((p[0]-a[0])*dx + (p[1]-a[1])*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(p[0]-(a[0]+t*dx), p[1]-(a[1]+t*dy))
}

// EncodePolyline encode au format Google (précision 1e-5).
func EncodePolyline(pts []Point) string {
	var sb strings.Builder
	var prevLat, prevLng int64
	for _, p := range pts {
		lat := int64(math.Round(p.Lat * 1e5))
		lng := int64(math.Round(p.Lng * 1e5))
		writeValue(&sb, lat-prevLat)
		writeValue(&sb, lng-prevLng)
		prevLat, prevLng = lat, lng
	}
	return sb.String()
}

func writeValue(sb *strings.Builder, v int64) {
	u := v << 1
	if v < 0 {
		u = ^u
	}
	for u >= 0x20 {
		sb.WriteByte(byte((0x20 | (u & 0x1f)) + 63))
		u >>= 5
	}
	sb.WriteByte(byte(u + 63))
}
