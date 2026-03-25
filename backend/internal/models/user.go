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

type User struct {
	ID           primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Email        string             `bson:"email" json:"email"`
	PasswordHash string             `bson:"password_hash" json:"-"`
	Strava       *StravaTokens      `bson:"strava,omitempty" json:"-"`
	CreatedAt    time.Time          `bson:"created_at" json:"created_at"`
}

func (u *User) HasStrava() bool {
	return u.Strava != nil && u.Strava.AccessToken != ""
}
