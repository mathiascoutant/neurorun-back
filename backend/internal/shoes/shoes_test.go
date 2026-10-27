package shoes

import (
	"testing"
	"time"
)

func TestCatalogueSansDoublonNiTrou(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range Catalog() {
		if m.ID == "" || m.Brand == "" || m.Name == "" {
			t.Fatalf("modèle incomplet : %+v", m)
		}
		if seen[m.ID] {
			t.Fatalf("identifiant en double : %s", m.ID)
		}
		seen[m.ID] = true
		if !ValidCategory(m.Category) || m.LifespanKm < 200 || m.LifespanKm > 1200 {
			t.Fatalf("catégorie ou durée de vie invraisemblable : %+v", m)
		}
	}
	if _, ok := Find("new-balance-fuelcell-sc-elite-v4"); !ok {
		t.Fatal("slug attendu introuvable")
	}
}

func TestUsureEtProjection(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	// 10 semaines, 300 km sur 700 : 30 km par semaine, 400 km restants.
	w := ComputeWear(300, 700, now.AddDate(0, 0, -70), now)
	if w.Status != StatusNew || w.KmPerWeek != 30 || w.RemainingKm != 400 {
		t.Fatalf("inattendu : %+v", w)
	}
	if w.WeeksLeft == nil || *w.WeeksLeft != 13.3 || w.ReplaceOn == "" {
		t.Fatalf("projection inattendue : %+v", w)
	}

	// Paire de la veille : le rythme se compte sur une semaine au moins.
	w = ComputeWear(10, 700, now.AddDate(0, 0, -1), now)
	if w.KmPerWeek != 10 {
		t.Fatalf("rythme d'une paire neuve : %+v", w)
	}

	// Durée de vie dépassée : plus de projection.
	w = ComputeWear(400, 350, now.AddDate(0, -4, 0), now)
	if w.Status != StatusWorn || w.WeeksLeft != nil || w.RemainingKm != 0 {
		t.Fatalf("paire usée : %+v", w)
	}

	// Jamais portée : pas de rythme, pas de date.
	w = ComputeWear(0, 700, now.AddDate(0, -1, 0), now)
	if w.KmPerWeek != 0 || w.WeeksLeft != nil {
		t.Fatalf("paire jamais portée : %+v", w)
	}
}
