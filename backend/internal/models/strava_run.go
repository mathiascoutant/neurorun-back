package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// StravaRun : une sortie Strava retenue pour le suivi des chaussures. Strava reste
// la source de la course ; on n'en garde que de quoi poser la question « avec
// quelle paire ? » et compter ses kilomètres sur la paire choisie.
type StravaRun struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID    primitive.ObjectID `bson:"user_id" json:"-"`
	StravaID  int64              `bson:"strava_id" json:"strava_id"`
	StartedAt time.Time          `bson:"started_at" json:"started_at"`
	Name      string             `bson:"name,omitempty" json:"name,omitempty"`
	DistanceM float64            `bson:"distance_m" json:"distance_m"`
	MovingSec int                `bson:"moving_sec" json:"moving_sec"`
	ElevGainM float64            `bson:"elevation_gain_m,omitempty" json:"elevation_gain_m,omitempty"`
	// Interval : Strava la marque comme séance (workout_type 3).
	Interval bool `bson:"interval,omitempty" json:"interval,omitempty"`
	// ShoeID / ShoeSkipped : même sens que sur live_runs.
	ShoeID      *primitive.ObjectID `bson:"shoe_id,omitempty" json:"shoe_id,omitempty"`
	ShoeSkipped bool                `bson:"shoe_skipped,omitempty" json:"-"`
	// Duplicate : copie Strava d'une course déjà enregistrée par NeuroRun. Elle
	// n'est ni demandée ni comptée : c'est la course NeuroRun qui porte la paire.
	Duplicate  bool      `bson:"duplicate,omitempty" json:"-"`
	ImportedAt time.Time `bson:"imported_at" json:"-"`
}
