package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type StravaTokens struct {
	AccessToken  string    `bson:"access_token" json:"-"`
	RefreshToken string    `bson:"refresh_token" json:"-"`
	ExpiresAt    time.Time `bson:"expires_at" json:"-"`
}

const (
	RoleUser  = "user"
	RoleAdmin = "admin"

	PlanStandard    = "standard"
	PlanStrava      = "strava"
	PlanPerformance = "performance"
)

type User struct {
	ID           primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Email        string             `bson:"email" json:"email"`
	PasswordHash string             `bson:"password_hash" json:"-"`
	Strava       *StravaTokens      `bson:"strava,omitempty" json:"-"`
	Role         string             `bson:"role,omitempty" json:"role"`
	Plan         string             `bson:"plan,omitempty" json:"plan"`
	CreatedAt    time.Time          `bson:"created_at" json:"created_at"`
	// LastSeenAt : dernière activité sur l’API (connexion ou requête authentifiée récente).
	LastSeenAt *time.Time `bson:"last_seen_at,omitempty" json:"last_seen_at,omitempty"`
}

func (u *User) EffectiveRole() string {
	if u.Role == RoleAdmin {
		return RoleAdmin
	}
	return RoleUser
}

func (u *User) EffectivePlan() string {
	if u.Plan == "" {
		return PlanStandard
	}
	if u.Plan == PlanStrava || u.Plan == PlanPerformance {
		return u.Plan
	}
	return PlanStandard
}

func (u *User) HasStrava() bool {
	return u.Strava != nil && u.Strava.AccessToken != ""
}
