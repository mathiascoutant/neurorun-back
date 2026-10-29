package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Appareil qui enregistre une course en direct. Il décide de la voie par laquelle
// les boosts reviennent au coureur : le téléphone et l'Apple Watch les relisent
// eux-mêmes, une montre Garmin ne sait que relayer les notifications du téléphone.
const (
	LiveSessionSourcePhone  = "phone"
	LiveSessionSourceWatch  = "watch"
	LiveSessionSourceGarmin = "garmin"
)

func ValidLiveSessionSource(s string) bool {
	return s == LiveSessionSourcePhone || s == LiveSessionSourceWatch || s == LiveSessionSourceGarmin
}

// LiveSession : une course en cours, annoncée au départ. Elle n'a ni trace ni
// distance — la course elle-même arrive plus tard dans live_runs — et ne sert
// qu'à prévenir les amis et à recevoir leurs boosts pendant l'effort.
type LiveSession struct {
	ID     primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID primitive.ObjectID `bson:"user_id" json:"-"`
	Source string             `bson:"source" json:"source"`
	// SessionType : free / endurance / interval, vide si inconnu au départ.
	SessionType string    `bson:"session_type,omitempty" json:"session_type,omitempty"`
	TargetKm    float64   `bson:"target_km,omitempty" json:"target_km,omitempty"`
	StartedAt   time.Time `bson:"started_at" json:"started_at"`
	// EndedAt : posé à l'arrêt, ou à l'arrivée de la course dans live_runs.
	EndedAt *time.Time `bson:"ended_at,omitempty" json:"ended_at,omitempty"`
	// ExpiresAt : une session jamais close (montre éteinte, app tuée) cesse d'être
	// « en direct » d'elle-même ; un index TTL la purge ensuite.
	ExpiresAt time.Time `bson:"expires_at" json:"-"`
}

// LiveBoost : un encouragement envoyé pendant la course. Contrairement au Boost
// d'une course terminée, on peut en envoyer plusieurs : chacun est un moment.
type LiveBoost struct {
	ID         primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	SessionID  primitive.ObjectID `bson:"session_id" json:"-"`
	RunnerID   primitive.ObjectID `bson:"runner_id" json:"-"`
	FromUserID primitive.ObjectID `bson:"from_user_id" json:"-"`
	Kind       string             `bson:"kind" json:"kind"`
	CreatedAt  time.Time          `bson:"created_at" json:"created_at"`
}
