package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// VmaTest : un test de VMA de 6 minutes. L'utilisateur peut le repasser quand
// il veut — on garde l'historique, la VMA courante est celle du test le plus
// récent.
type VmaTest struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID    primitive.ObjectID `bson:"user_id" json:"-"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`

	// ClientTestID : clé d'idempotence générée par l'app ou la montre, pour
	// qu'un réessai d'envoi ne crée pas un second test.
	ClientTestID string `bson:"client_test_id,omitempty" json:"client_test_id,omitempty"`

	DistanceM   float64 `bson:"distance_m" json:"distance_m"`
	DurationSec float64 `bson:"duration_sec" json:"duration_sec"`
	VmaKmh      float64 `bson:"vma_kmh" json:"vma_kmh"`

	// Source : "iphone" ou "watch".
	Source      string              `bson:"source,omitempty" json:"source,omitempty"`
	TrackPoints []LiveRunTrackPoint `bson:"track_points,omitempty" json:"-"`
}

// VmaTestListItem : résumé pour l'historique.
type VmaTestListItem struct {
	ID          string  `json:"id"`
	CreatedAt   string  `json:"created_at"`
	DistanceM   float64 `json:"distance_m"`
	DurationSec float64 `json:"duration_sec"`
	VmaKmh      float64 `json:"vma_kmh"`
	Source      string  `json:"source,omitempty"`
}
