package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Statuts d'une demande d'ami. Un refus supprime la ligne au lieu de la marquer :
// sans ça, un refus interdirait définitivement une nouvelle demande plus tard.
const (
	FriendshipPending  = "pending"
	FriendshipAccepted = "accepted"
)

// Friendship : une ligne par paire, dans les deux sens. Requester est celui qui a
// envoyé la demande ; seul Addressee peut l'accepter.
type Friendship struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	RequesterID primitive.ObjectID `bson:"requester_id" json:"-"`
	AddresseeID primitive.ObjectID `bson:"addressee_id" json:"-"`
	Status      string             `bson:"status" json:"status"`
	CreatedAt   time.Time          `bson:"created_at" json:"created_at"`
	RespondedAt *time.Time         `bson:"responded_at,omitempty" json:"responded_at,omitempty"`
}

// Other renvoie l'autre membre de la paire vu depuis self.
func (f *Friendship) Other(self primitive.ObjectID) primitive.ObjectID {
	if f.RequesterID == self {
		return f.AddresseeID
	}
	return f.RequesterID
}

// Réactions possibles sur une course d'ami.
const (
	BoostKindBoost   = "boost"
	BoostKindFire    = "fire"
	BoostKindRespect = "respect"
)

func ValidBoostKind(k string) bool {
	return k == BoostKindBoost || k == BoostKindFire || k == BoostKindRespect
}

// Boost : réaction d'un utilisateur sur la course d'un ami. Une seule par
// (course, émetteur) — changer de réaction met à jour la même ligne, ce qui
// évite qu'un aller-retour sur les boutons spamme le propriétaire de notifs.
type Boost struct {
	ID         primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	RunID      primitive.ObjectID `bson:"run_id" json:"-"`
	RunOwnerID primitive.ObjectID `bson:"run_owner_id" json:"-"`
	FromUserID primitive.ObjectID `bson:"from_user_id" json:"-"`
	Kind       string             `bson:"kind" json:"kind"`
	CreatedAt  time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt  time.Time          `bson:"updated_at" json:"updated_at"`
}

// PushToken : appareil d'un utilisateur pour les notifications sociales.
// Distinct de admin_push_tokens, qui ne sert qu'aux alertes d'inscription.
type PushToken struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"-"`
	Token     string             `bson:"token" json:"token"`
	UserID    primitive.ObjectID `bson:"user_id" json:"-"`
	Platform  string             `bson:"platform,omitempty" json:"platform,omitempty"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time          `bson:"updated_at" json:"updated_at"`
}
