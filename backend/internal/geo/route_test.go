package geo

import (
	"math"
	"testing"
)

func TestEncodePolylineReference(t *testing.T) {
	// Exemple de la documentation Google.
	pts := []Point{{38.5, -120.2}, {40.7, -120.95}, {43.252, -126.453}}
	want := "_p~iF~ps|U_ulLnnqC_mqNvxq`@"
	if got := EncodePolyline(pts); got != want {
		t.Fatalf("EncodePolyline = %q, attendu %q", got, want)
	}
}

func TestSimplifyKeepsShapeAndBound(t *testing.T) {
	// Boucle de ~2 km échantillonnée à la seconde : 1 800 points.
	var pts []Point
	for i := 0; i < 1800; i++ {
		a := float64(i) / 1800 * 2 * math.Pi
		pts = append(pts, Point{Lat: 48.85 + 0.003*math.Sin(a), Lng: 2.35 + 0.0045*math.Cos(a)})
	}
	out := Simplify(pts, maxRoutePoints)
	if len(out) > maxRoutePoints || len(out) < 20 {
		t.Fatalf("Simplify : %d points, attendu entre 20 et %d", len(out), maxRoutePoints)
	}
	if out[0] != pts[0] || out[len(out)-1] != pts[len(pts)-1] {
		t.Fatal("Simplify doit garder le départ et l'arrivée")
	}
}

func TestRoutePolylineSkipsMissingTrack(t *testing.T) {
	if got := RoutePolyline(nil); got != "" {
		t.Fatalf("trace vide : %q", got)
	}
	if got := RoutePolyline([]Point{{0, 0}, {0, 0}, {48.85, 2.35}}); got != "" {
		t.Fatalf("un seul point valide : %q", got)
	}
}
