package handlers

import (
	"strings"
	"testing"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestFriendRunRecipients(t *testing.T) {
	author := primitive.NewObjectID()
	a, b := primitive.NewObjectID(), primitive.NewObjectID()

	got := friendRunRecipients(author, []primitive.ObjectID{a, author, b, a, primitive.NilObjectID})
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("destinataires = %v, attendu [%s %s] (sans l'auteur, sans doublon ni id vide)", got, a.Hex(), b.Hex())
	}
	if got := friendRunRecipients(author, nil); len(got) != 0 {
		t.Errorf("sans ami : %v, attendu aucun destinataire", got)
	}
}

func TestShouldNotifyFriendsOfRun(t *testing.T) {
	now := time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)
	endedAgo := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }

	cases := []struct {
		name string
		run  models.LiveRun
		want bool
	}{
		{"sortie de 5 km tout juste finie", models.LiveRun{DistanceM: 5000, GpsEndTsMs: endedAgo(5 * time.Minute)}, true},
		{"pile 1 km", models.LiveRun{DistanceM: 1000, GpsEndTsMs: endedAgo(time.Minute)}, true},
		{"trop courte pour une vraie sortie", models.LiveRun{DistanceM: 850, GpsEndTsMs: endedAgo(time.Minute)}, false},
		{"course de montre restée en file deux jours", models.LiveRun{DistanceM: 10000, GpsEndTsMs: endedAgo(48 * time.Hour)}, false},
		// Sans horodatage GPS, la fin se déduit de l'enregistrement et de la durée.
		{"sans GPS, fin déduite", models.LiveRun{DistanceM: 3000, CreatedAt: now.Add(-40 * time.Minute), WallSec: 1800}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldNotifyFriendsOfRun(&c.run, now); got != c.want {
				t.Errorf("shouldNotifyFriendsOfRun = %v, attendu %v", got, c.want)
			}
		})
	}
}

func TestFriendRunNotificationText(t *testing.T) {
	score := func(n int) *int { return &n }

	cases := []struct {
		name      string
		distanceM float64
		score     *int
		title     string
		bodyHas   []string
		bodyHasNo []string
	}{
		{"5 km sans note", 5030, nil, "🏃 Louis vient de courir 5 km", []string{"Belle sortie !", "Va l’encourager 👏"}, []string{"/100"}},
		{"courte sortie au dixième", 3240, nil, "🏃 Louis vient de courir 3,2 km", []string{"Chaque sortie compte."}, nil},
		{"10 km arrondi au km", 10460, nil, "💪 Louis vient de courir 10 km", []string{"Une vraie sortie"}, nil},
		{"semi", 21097, nil, "🔥 Louis vient d’avaler 21 km", []string{"grosse sortie"}, nil},
		{"marathon", 42195, nil, "🏅 Louis vient de courir 42 km !", []string{"marathonien"}, nil},
		{"très bonne note citée", 8000, score(93), "🏃 Louis vient de courir 8 km", []string{"Note de 93/100, exceptionnelle !"}, []string{"Belle sortie"}},
		{"bonne note ajoutée au compliment", 8000, score(80), "🏃 Louis vient de courir 8 km", []string{"Belle sortie ! Note de 80/100."}, nil},
		{"note moyenne tue", 8000, score(52), "🏃 Louis vient de courir 8 km", []string{"Belle sortie !"}, []string{"52"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			title, body := friendRunNotificationText("Louis", c.distanceM, c.score)
			if title != c.title {
				t.Errorf("titre = %q, attendu %q", title, c.title)
			}
			if !strings.HasSuffix(body, "Va l’encourager 👏") {
				t.Errorf("corps = %q, doit finir par l'invitation à encourager", body)
			}
			for _, s := range c.bodyHas {
				if !strings.Contains(body, s) {
					t.Errorf("corps = %q, doit contenir %q", body, s)
				}
			}
			for _, s := range c.bodyHasNo {
				if strings.Contains(body, s) {
					t.Errorf("corps = %q, ne doit pas contenir %q", body, s)
				}
			}
		})
	}
}

func TestFormatRoundedKm(t *testing.T) {
	cases := map[float64]string{
		1000:  "1 km",
		4960:  "5 km",
		7460:  "7,5 km",
		9990:  "10 km",
		12600: "13 km",
	}
	for m, want := range cases {
		if got := formatRoundedKm(m); got != want {
			t.Errorf("formatRoundedKm(%v) = %q, attendu %q", m, got, want)
		}
	}
}
