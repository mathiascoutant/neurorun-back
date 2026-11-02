package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	ShopModel3DIdle       = "idle"
	ShopModel3DGenerating = "generating"
)

// ShopModel3D : travail de modélisation d'un article. À part de `shop_products` : le catalogue public
// n'a pas à charger les photos de travail ni les scènes.
type ShopModel3D struct {
	ProductID primitive.ObjectID `bson:"_id"`
	// Photos : noms de fichiers dans ShopMediaDir. Elles ne font pas partie de la galerie du produit.
	Photos []string `bson:"photos"`
	// Versions : de la plus ancienne à la plus récente ; la dernière est la scène actuelle.
	Versions  []ShopModel3DVersion `bson:"versions"`
	Status    string               `bson:"status"`
	StartedAt time.Time            `bson:"started_at,omitempty"`
	Error     string               `bson:"error,omitempty"`
	// Published : scène montrée sur la boutique, figée au moment de la publication : retoucher ne
	// change pas ce que voient les clients tant qu'on ne republie pas.
	Published string `bson:"published,omitempty"`
	// PublishedPhotos : photos dont cette scène reprend les impressions. Elles deviennent publiques.
	PublishedPhotos []string `bson:"published_photos,omitempty"`
}

type ShopModel3DVersion struct {
	Instruction string    `bson:"instruction,omitempty"`
	Summary     string    `bson:"summary"`
	Scene       string    `bson:"scene"`
	CreatedAt   time.Time `bson:"created_at"`
}
