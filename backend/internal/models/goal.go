package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Goal struct {
	ID              primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID          primitive.ObjectID `bson:"user_id" json:"-"` // jamais exposé au client
	DistanceKm      float64            `bson:"distance_km" json:"distance_km"`
	DistanceLabel   string             `bson:"distance_label" json:"distance_label"`
	Weeks           int                `bson:"weeks" json:"weeks"`
	SessionsPerWeek int                `bson:"sessions_per_week" json:"sessions_per_week"`
	TargetTime      string             `bson:"target_time,omitempty" json:"target_time"`
	Plan            string             `bson:"plan" json:"plan"`
	CoachThread     []ChatTurn         `bson:"coach_thread,omitempty" json:"coach_thread,omitempty"`
	CreatedAt       time.Time          `bson:"created_at" json:"created_at"`
}
