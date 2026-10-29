package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Ce que propose le coach face à une mauvaise météo : déplacer la séance vers un
// jour plus clément, ou, faute de jour libre, la courir autrement.
const (
	CoachSuggestionMove  = "move"
	CoachSuggestionAdapt = "adapt"
)

// Réponse du coureur. Le coach ne change jamais le plan de lui-même : seule une
// suggestion acceptée déplace la séance.
const (
	CoachSuggestionPending   = "pending"
	CoachSuggestionAccepted  = "accepted"
	CoachSuggestionDismissed = "dismissed"
)

// CoachSuggestion : une proposition du coach pour la séance d'un jour donné. Une
// seule par objectif et par jour : relancer le calcul ne la duplique pas, et ne
// renvoie donc pas la notification.
type CoachSuggestion struct {
	ID      primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID  primitive.ObjectID `bson:"user_id" json:"-"`
	GoalID  primitive.ObjectID `bson:"goal_id" json:"goal_id"`
	Day     string             `bson:"day" json:"day"`
	Week    int                `bson:"week" json:"week"`
	Session int                `bson:"session" json:"session"`
	Kind    string             `bson:"kind" json:"kind"`
	// Hazard : weather.Hazard de la journée (ice, storm, heat…).
	Hazard string `bson:"hazard" json:"hazard"`
	// ToDate : jour proposé, pour un déplacement.
	ToDate string `bson:"to_date,omitempty" json:"to_date,omitempty"`
	Title  string `bson:"title" json:"title"`
	Body   string `bson:"body" json:"body"`
	// Tip : comment courir quand même, si la séance reste au jour prévu.
	Tip        string     `bson:"tip,omitempty" json:"tip,omitempty"`
	Status     string     `bson:"status" json:"status"`
	CreatedAt  time.Time  `bson:"created_at" json:"created_at"`
	AnsweredAt *time.Time `bson:"answered_at,omitempty" json:"answered_at,omitempty"`
}
