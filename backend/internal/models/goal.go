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

// SessionOverride fixe le sort d'une séance précise, décidé avec le coach : date
// imposée (report) ou annulation. Prime sur le motif hebdomadaire et sur le
// décalage automatique lié à une indisponibilité.
type SessionOverride struct {
	Week    int    `bson:"week" json:"week"`
	Session int    `bson:"session" json:"session"`
	// Date au format AAAA-MM-JJ (jour civil, fuseau du calendrier).
	Date    string `bson:"date,omitempty" json:"date,omitempty"`
	Skipped bool   `bson:"skipped,omitempty" json:"skipped,omitempty"`
	Reason  string `bson:"reason,omitempty" json:"reason,omitempty"`
}

// Unavailability est une période où la personne ne peut pas courir (maladie,
// déplacement, blessure). Les séances qui y tombent sont reportées au premier
// jour disponible suivant.
type Unavailability struct {
	From   string `bson:"from" json:"from"`
	To     string `bson:"to" json:"to"`
	Reason string `bson:"reason,omitempty" json:"reason,omitempty"`
}

// Terrain d'une course. Une prépa de trail ne se chiffre pas comme une prépa de
// route : le dénivelé prime sur l'allure, la marche en montée fait partie de la
// course, et les min/km d'une sortie sur sentier ne se comparent pas au bitume.
const (
	GoalTerrainRoad  = "road"
	GoalTerrainTrail = "trail"
)

// GoalTerrainLabel décrit le terrain au coach, en français : c'est ce libellé qui
// part dans les consignes de rédaction du plan.
func GoalTerrainLabel(terrain string) string {
	if terrain == GoalTerrainTrail {
		return "trail (chemins et sentiers, dénivelé marqué)"
	}
	return "course à pied sur route (bitume, profil roulant)"
}

type Goal struct {
	ID               primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID           primitive.ObjectID `bson:"user_id" json:"-"` // jamais exposé au client
	DistanceKm       float64            `bson:"distance_km" json:"distance_km"`
	DistanceLabel    string             `bson:"distance_label" json:"distance_label"`
	// Terrain : "road" ou "trail". Absent sur les objectifs créés avant ce champ —
	// passer par EffectiveTerrain plutôt que de lire la valeur brute.
	Terrain          string             `bson:"terrain,omitempty" json:"terrain,omitempty"`
	// RaceDate : jour de la course au format AAAA-MM-JJ, quand la personne l'a indiqué
	// plutôt qu'un nombre de semaines. C'est la vraie échéance : Weeks n'en est qu'une
	// approximation arrondie à la semaine. Vide = échéance déduite de Weeks.
	RaceDate         string             `bson:"race_date,omitempty" json:"race_date,omitempty"`
	// CustomTitle : libellé choisi par l’utilisateur (affiché à la place de distance_label si non vide).
	CustomTitle      string             `bson:"custom_title,omitempty" json:"custom_title,omitempty"`
	Weeks            int                `bson:"weeks" json:"weeks"`
	SessionsPerWeek  int                `bson:"sessions_per_week" json:"sessions_per_week"`
	TargetTime       string             `bson:"target_time,omitempty" json:"target_time"`
	Plan             string             `bson:"plan" json:"plan"`
	PlannedSessions  []PlannedSession   `bson:"planned_sessions,omitempty" json:"planned_sessions,omitempty"`
	// CalendarDayOffsets : jours 0=lundi…6=dimanche ; si absent ou longueur ≠ sessions_per_week, on utilise le motif par défaut.
	CalendarDayOffsets []int `bson:"calendar_day_offsets,omitempty" json:"calendar_day_offsets,omitempty"`
	// SessionOverrides : reports et annulations séance par séance décidés avec le coach.
	SessionOverrides []SessionOverride `bson:"session_overrides,omitempty" json:"session_overrides,omitempty"`
	// Unavailabilities : périodes sans course ; les séances concernées sont décalées.
	Unavailabilities []Unavailability `bson:"unavailabilities,omitempty" json:"unavailabilities,omitempty"`
	CoachThread      []ChatTurn         `bson:"coach_thread,omitempty" json:"coach_thread,omitempty"`
	// PlanWithoutStravaData : plan généré sans JSON d’activités (objectif seul) ; repasse à false après régénération avec Strava.
	PlanWithoutStravaData bool `bson:"plan_without_strava_data,omitempty" json:"plan_without_strava_data,omitempty"`
	CreatedAt        time.Time          `bson:"created_at" json:"created_at"`
}

// EffectiveTerrain : les objectifs antérieurs à ce champ décrivent tous une course
// sur route, et toute valeur inattendue retombe sur ce même défaut.
func (g *Goal) EffectiveTerrain() string {
	if g.Terrain == GoalTerrainTrail {
		return GoalTerrainTrail
	}
	return GoalTerrainRoad
}
