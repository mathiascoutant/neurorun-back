package strava

import (
	"encoding/xml"
	"math"
	"testing"
	"time"

	"runapp/internal/models"
)

func f64(v float64) *float64 { return &v }

func TestBuildTCXIsReadableAndMatchesNeuroRunDistance(t *testing.T) {
	start := time.Date(2026, 10, 28, 7, 30, 0, 0, time.UTC).UnixMilli()
	lr := &models.LiveRun{
		DistanceM: 1000,
		MovingSec: 300,
		TrackPoints: []models.LiveRunTrackPoint{
			{Lat: 48.8566, Lng: 2.3522, TMs: start + 2000, HrBpm: f64(141.6)},
			{Lat: 48.8566, Lng: 2.3522, TMs: start + 2000}, // horodatage répété : écarté
			{Lat: 48.8500, Lng: 2.3522, TMs: start},        // arrivé dans le désordre
			{Lat: 0, Lng: 0, TMs: start + 3000},            // sans position : écarté
			{Lat: 48.8600, Lng: 2.3522, TMs: start + 4000, AltM: f64(35)},
		},
	}
	var doc struct {
		Laps []struct {
			Distance float64 `xml:"DistanceMeters"`
			Points   []struct {
				Time     string   `xml:"Time"`
				Distance float64  `xml:"DistanceMeters"`
				HR       *int     `xml:"HeartRateBpm>Value"`
				Alt      *float64 `xml:"AltitudeMeters"`
			} `xml:"Track>Trackpoint"`
		} `xml:"Activities>Activity>Lap"`
	}
	if err := xml.Unmarshal(BuildTCX(lr), &doc); err != nil {
		t.Fatalf("TCX illisible : %v", err)
	}
	if len(doc.Laps) != 1 || len(doc.Laps[0].Points) != 3 {
		t.Fatalf("attendu 1 tour de 3 points, obtenu %+v", doc.Laps)
	}
	pts := doc.Laps[0].Points
	if pts[0].Time != "2026-10-28T07:30:00.000Z" {
		t.Errorf("premier point = %s, le fichier doit suivre l'ordre du temps", pts[0].Time)
	}
	if math.Abs(pts[2].Distance-1000) > 0.5 || doc.Laps[0].Distance != 1000 {
		t.Errorf("distance finale %.1f, attendu la distance NeuroRun (1000)", pts[2].Distance)
	}
	if pts[1].HR == nil || *pts[1].HR != 142 {
		t.Errorf("fréquence cardiaque perdue : %+v", pts[1].HR)
	}
	if pts[2].Alt == nil || *pts[2].Alt != 35 {
		t.Errorf("altitude perdue : %+v", pts[2].Alt)
	}
}

func TestHasTCXTrackNeedsTwoPositionedPoints(t *testing.T) {
	lr := &models.LiveRun{TrackPoints: []models.LiveRunTrackPoint{{Lat: 48.85, Lng: 2.35, TMs: 1}, {TMs: 2}}}
	if HasTCXTrack(lr) {
		t.Error("un seul point GPS ne fait pas un tracé")
	}
}

func TestUploadErrorAsDuplicate(t *testing.T) {
	for msg, want := range map[string]int64{
		"neurorun-abc.tcx duplicate of activity 12345678901":              12345678901,
		"neurorun-abc.tcx duplicate of <a href='/activities/987'>987</a>": 987,
	} {
		dup, ok := uploadErrorAsDuplicate(msg)
		if !ok || dup.DuplicateOf != want {
			t.Errorf("%q → %+v, attendu %d", msg, dup, want)
		}
	}
	if _, ok := uploadErrorAsDuplicate("There was an error processing your activity."); ok {
		t.Error("une vraie erreur n'est pas un doublon")
	}
}
