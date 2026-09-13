package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// RefreshToken : session longue durée d’un appareil. Le jeton lui-même n’est
// jamais stocké — seul son SHA-256 l’est, pour qu’une fuite de la base ne donne
// pas de sessions utilisables. Chaque usage le remplace (rotation) : un jeton
// consommé deux fois signale un vol, et toute la lignée est alors révoquée.
type RefreshToken struct {
	ID primitive.ObjectID `bson:"_id,omitempty" json:"-"`
	// TokenHash : hex du SHA-256 du jeton remis au client.
	TokenHash string             `bson:"token_hash" json:"-"`
	UserID    primitive.ObjectID `bson:"user_id" json:"-"`
	// FamilyID : identifiant de la lignée née d’une connexion. La rotation le
	// conserve, ce qui permet de tout couper d’un coup en cas de rejeu.
	FamilyID  primitive.ObjectID `bson:"family_id" json:"-"`
	CreatedAt time.Time          `bson:"created_at" json:"-"`
	ExpiresAt time.Time          `bson:"expires_at" json:"-"`
	// UsedAt : horodatage de la rotation. Non nul = jeton déjà échangé, donc
	// invalide ; le revoir passer est une tentative de rejeu.
	UsedAt *time.Time `bson:"used_at,omitempty" json:"-"`
	// RevokedAt : coupé par une déconnexion, un changement de mot de passe ou
	// la détection d’un rejeu.
	RevokedAt *time.Time `bson:"revoked_at,omitempty" json:"-"`
	// Device : libellé indicatif envoyé par le client, pour l’écran « appareils ».
	Device string `bson:"device,omitempty" json:"-"`
}

// Valid : jeton encore échangeable à l’instant `now`.
func (t *RefreshToken) Valid(now time.Time) bool {
	return t.UsedAt == nil && t.RevokedAt == nil && now.Before(t.ExpiresAt)
}
