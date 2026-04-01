package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// PlannedSession est une séance du plan (distance mini sur Strava, allure cible optionnelle).
type PlannedSession struct {
	Week         int      `bson:"week" json:"week"`
	Session      int      `bson:"session" json:"session"`
	DistanceKm   float64  `bson:"distance_km" json:"distance_km"`
	PaceSecPerKm *float64 `bson:"pace_sec_per_km,omitempty" json:"pace_sec_per_km,omitempty"`
	Summary      string   `bson:"summary,omitempty" json:"summary,omitempty"`
}

type Goal struct {
	ID               primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID           primitive.ObjectID `bson:"user_id" json:"-"` // jamais exposé au client
	DistanceKm       float64            `bson:"distance_km" json:"distance_km"`
	DistanceLabel    string             `bson:"distance_label" json:"distance_label"`
	Weeks            int                `bson:"weeks" json:"weeks"`
	SessionsPerWeek  int                `bson:"sessions_per_week" json:"sessions_per_week"`
	TargetTime       string             `bson:"target_time,omitempty" json:"target_time"`
	Plan             string             `bson:"plan" json:"plan"`
	PlannedSessions  []PlannedSession   `bson:"planned_sessions,omitempty" json:"planned_sessions,omitempty"`
	// CalendarDayOffsets : jours 0=lundi…6=dimanche ; si absent ou longueur ≠ sessions_per_week, on utilise le motif par défaut.
	CalendarDayOffsets []int `bson:"calendar_day_offsets,omitempty" json:"calendar_day_offsets,omitempty"`
	CoachThread      []ChatTurn         `bson:"coach_thread,omitempty" json:"coach_thread,omitempty"`
	// PlanWithoutStravaData : plan généré sans JSON d’activités (objectif seul) ; repasse à false après régénération avec Strava.
	PlanWithoutStravaData bool `bson:"plan_without_strava_data,omitempty" json:"plan_without_strava_data,omitempty"`
	CreatedAt        time.Time          `bson:"created_at" json:"created_at"`
}
