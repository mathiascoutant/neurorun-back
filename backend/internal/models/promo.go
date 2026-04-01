package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// PromoCode : réduction en % sur l’abonnement mensuel (paiement simulé côté app).
type PromoCode struct {
	ID              primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	Code            string             `bson:"code" json:"code"`
	PercentOff      int                `bson:"percent_off" json:"percent_off"` // 0–100
	MaxUses         int                `bson:"max_uses" json:"max_uses"`       // 0 = illimité
	Uses            int                `bson:"uses" json:"uses"`
	ExpiresAt       *time.Time         `bson:"expires_at,omitempty" json:"expires_at,omitempty"`
	Active          bool               `bson:"active" json:"active"`
	ApplicablePlans []string           `bson:"applicable_plans,omitempty" json:"applicable_plans,omitempty"` // vide = tous
	CreatedAt       time.Time          `bson:"created_at" json:"created_at"`
}
