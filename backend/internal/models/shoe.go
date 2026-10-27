package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// MaxShoesPerUser : au-delà, c'est une collection, pas un suivi d'usure.
const MaxShoesPerUser = 20

// Shoe : une paire de chaussures du coureur.
//
// Les kilomètres d'une paire = InitialKm + la distance des courses NeuroRun
// qui lui sont rattachées (live_runs.shoe_id). InitialKm couvre tout ce qui
// précède l'ajout : à la création, l'app le propose à partir de l'historique
// depuis StartedOn, et le coureur peut le corriger.
type Shoe struct {
	ID     primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID primitive.ObjectID `bson:"user_id" json:"-"`

	Brand string `bson:"brand" json:"brand"`
	Model string `bson:"model" json:"model"`
	// Nickname : surnom facultatif (« les rouges »), pour distinguer deux
	// paires du même modèle.
	Nickname string `bson:"nickname,omitempty" json:"nickname,omitempty"`
	// CatalogID : modèle du catalogue choisi ; vide pour une saisie libre.
	CatalogID string `bson:"catalog_id,omitempty" json:"catalog_id,omitempty"`
	Category  string `bson:"category" json:"category"`
	// LifespanKm : durée de vie théorique retenue (catalogue ou saisie).
	LifespanKm float64 `bson:"lifespan_km" json:"lifespan_km"`
	// StartedOn : jour civil AAAA-MM-JJ depuis lequel le coureur a la paire.
	StartedOn string  `bson:"started_on" json:"started_on"`
	InitialKm float64 `bson:"initial_km" json:"initial_km"`
	// RetiredAt : paire mise au placard. Elle reste visible mais n'est plus
	// proposée à la fin d'une course.
	RetiredAt *time.Time `bson:"retired_at,omitempty" json:"retired_at,omitempty"`
	CreatedAt time.Time  `bson:"created_at" json:"created_at"`
}

// ShoeRunTotal : distance des courses rattachées à une paire.
type ShoeRunTotal struct {
	ShoeID    primitive.ObjectID `bson:"_id"`
	DistanceM float64            `bson:"distance_m"`
	Runs      int                `bson:"runs"`
}
