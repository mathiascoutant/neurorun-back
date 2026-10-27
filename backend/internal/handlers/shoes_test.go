package handlers

import (
	"testing"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// On ne demande la paire que pour les courses postérieures au premier ajout,
// et jamais au-delà de deux semaines.
func TestPendingShoeSince(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-3 * 24 * time.Hour)
	old := now.AddDate(0, -2, 0)

	got := pendingShoeSince([]shoeView{{Shoe: models.Shoe{CreatedAt: recent}}}, now)
	if !got.Equal(recent) {
		t.Fatalf("paire récente : %v, attendu %v", got, recent)
	}
	got = pendingShoeSince([]shoeView{{Shoe: models.Shoe{CreatedAt: old}}}, now)
	if want := now.Add(-pendingShoeWindow); !got.Equal(want) {
		t.Fatalf("paire ancienne : %v, attendu %v", got, want)
	}
}

// Kilomètres d'une paire = déjà faits à l'ajout + courses rattachées ; les
// paires au placard passent en fin de liste.
func TestBuildShoeViews(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	retired := now.AddDate(0, -1, 0)
	a := models.Shoe{ID: primitive.NewObjectID(), StartedOn: "2026-08-01", InitialKm: 100, LifespanKm: 700, RetiredAt: &retired}
	b := models.Shoe{ID: primitive.NewObjectID(), StartedOn: "2026-09-01", InitialKm: 50, LifespanKm: 350}
	totals := map[primitive.ObjectID]models.ShoeRunTotal{
		b.ID: {ShoeID: b.ID, DistanceM: 25_500, Runs: 3},
	}
	views := buildShoeViews([]models.Shoe{a, b}, totals, now)
	if views[0].ID != b.ID || !views[1].Retired {
		t.Fatalf("ordre inattendu : %+v", views)
	}
	if views[0].Wear.UsedKm != 75.5 || views[0].Runs != 3 {
		t.Fatalf("kilomètres inattendus : %+v", views[0].Wear)
	}
}
