package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// StravaBestEfforts : portions chronométrées par Strava dans une sortie (5 km,
// 10 km…), lues une fois pour toutes : le détail d'une activité coûte une
// requête sous quota, et une sortie passée ne change plus.
type StravaBestEfforts struct {
	ID       primitive.ObjectID `bson:"_id,omitempty"`
	UserID   primitive.ObjectID `bson:"user_id"`
	StravaID int64              `bson:"strava_id"`
	// Efforts : par identifiant de distance (« 5k », « half »…). Vide quand la
	// sortie n'en a aucune : elle n'est pas redemandée pour autant.
	Efforts   map[string]StravaEffort `bson:"efforts"`
	FetchedAt time.Time               `bson:"fetched_at"`
}

// StravaEffort : une portion, en temps écoulé, et son départ.
type StravaEffort struct {
	TimeSec float64   `bson:"time_sec"`
	StartAt time.Time `bson:"start_at"`
}
