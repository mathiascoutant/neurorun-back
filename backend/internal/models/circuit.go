package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// LatLng point GPS (WGS84).
type LatLng struct {
	Lat float64 `json:"lat" bson:"lat"`
	Lng float64 `json:"lng" bson:"lng"`
}

// Circuit : parcours défini par une polyligne ordonnée et un indice de départ (boucle).
type Circuit struct {
	ID         primitive.ObjectID `json:"id" bson:"_id,omitempty"`
	Name       string             `json:"name" bson:"name"`
	Points     []LatLng           `json:"points" bson:"points"`
	StartIndex int                `json:"start_index" bson:"start_index"`
	Center     GeoPoint           `json:"center" bson:"center"`
	CreatedBy  primitive.ObjectID `json:"created_by" bson:"created_by"`
	CreatedAt  time.Time          `json:"created_at" bson:"created_at"`
}

// GeoPoint : Point GeoJSON pour index 2dsphere (coordinates [lng, lat]).
type GeoPoint struct {
	Type        string    `json:"type" bson:"type"`
	Coordinates []float64 `json:"coordinates" bson:"coordinates"`
}

func NewGeoPoint(lng, lat float64) GeoPoint {
	return GeoPoint{Type: "Point", Coordinates: []float64{lng, lat}}
}

// CircuitTime : passage chronométré sur un circuit.
type CircuitTime struct {
	ID         primitive.ObjectID `json:"id" bson:"_id,omitempty"`
	CircuitID  primitive.ObjectID `json:"circuit_id" bson:"circuit_id"`
	UserID     primitive.ObjectID `json:"user_id" bson:"user_id"`
	DurationMs int64              `json:"duration_ms" bson:"duration_ms"`
	CreatedAt  time.Time          `json:"created_at" bson:"created_at"`
}
