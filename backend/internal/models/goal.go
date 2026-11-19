package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Natures de séance. Elles décident de la couleur et du pictogramme dans le
// calendrier, et de ce que la montre fait toute seule (tours d'un fractionné).
const (
	SessionKindEasy     = "easy"     // footing, endurance fondamentale
	SessionKindLong     = "long"     // sortie longue
	SessionKindInterval = "interval" // fractionné
	SessionKindTempo    = "tempo"    // seuil, allure spécifique
	SessionKindHills    = "hills"    // côtes, dénivelé
	SessionKindRecovery = "recovery" // récupération
	SessionKindRace     = "race"     // course prévue pendant la prépa
)

// NormalizeSessionKind : nature connue, sinon vide (la séance s'affiche sans étiquette).
func NormalizeSessionKind(k string) string {
	switch k {
	case SessionKindEasy, SessionKindLong, SessionKindInterval, SessionKindTempo,
		SessionKindHills, SessionKindRecovery, SessionKindRace:
		return k
	default:
		return ""
	}
}

// Étapes d'une séance guidée.
const (
	StepWarmup   = "warmup"
	StepRun      = "run"     // effort
	StepRecover  = "recover" // récupération entre deux efforts
	StepCooldown = "cooldown"
)

// WorkoutStep : une étape de séance, que la montre enchaîne toute seule. Elle se
// termine à la distance OU à la durée indiquée (une seule des deux est renseignée).
type WorkoutStep struct {
	Kind         string   `bson:"kind" json:"kind"`
	DistanceM    float64  `bson:"distance_m,omitempty" json:"distance_m,omitempty"`
	DurationSec  float64  `bson:"duration_sec,omitempty" json:"duration_sec,omitempty"`
	PaceSecPerKm *float64 `bson:"pace_sec_per_km,omitempty" json:"pace_sec_per_km,omitempty"`
	// Label : ce que la montre affiche (« 400 m vite · 3/6 »).
	Label string `bson:"label,omitempty" json:"label,omitempty"`
}

// PlannedSession est une séance du plan (distance mini sur Strava, allure cible optionnelle).
type PlannedSession struct {
	Week         int      `bson:"week" json:"week"`
	Session      int      `bson:"session" json:"session"`
	DistanceKm   float64  `bson:"distance_km" json:"distance_km"`
	PaceSecPerKm *float64 `bson:"pace_sec_per_km,omitempty" json:"pace_sec_per_km,omitempty"`
	Summary      string   `bson:"summary,omitempty" json:"summary,omitempty"`
	// Kind : nature de la séance (SessionKind…). Vide sur les plans d'avant ce champ.
	Kind string `bson:"kind,omitempty" json:"kind,omitempty"`
	// Detail : déroulé complet, quand le coach a réécrit la séance. Il prime alors
	// sur le texte du plan Markdown, qui décrit l'ancienne version.
	Detail string `bson:"detail,omitempty" json:"detail,omitempty"`
	// Edited : séance modifiée après la rédaction du plan (par le coach ou une course).
	Edited bool `bson:"edited,omitempty" json:"edited,omitempty"`
	// RaceID : la séance est une course prévue (Goal.Races).
	RaceID string `bson:"race_id,omitempty" json:"race_id,omitempty"`
	// Steps : étapes pour la montre, calculées à la première demande puis gardées.
	// Effacées dès que le contenu de la séance change.
	Steps []WorkoutStep `bson:"steps,omitempty" json:"steps,omitempty"`
}

// GoalRace : une course que la personne a prévue pendant sa préparation (un 10 km,
// un semi avant le marathon). Elle prend la place d'une séance du plan.
type GoalRace struct {
	ID         string  `bson:"id" json:"id"`
	Name       string  `bson:"name" json:"name"`
	Date       string  `bson:"date" json:"date"` // AAAA-MM-JJ
	DistanceKm float64 `bson:"distance_km" json:"distance_km"`
	TargetTime string  `bson:"target_time,omitempty" json:"target_time,omitempty"`
	// Official : épreuve avec dossard, par opposition à un défi personnel.
	Official bool `bson:"official,omitempty" json:"official,omitempty"`
	// Week / Session : la séance du plan que la course remplace.
	Week    int `bson:"week" json:"week"`
	Session int `bson:"session" json:"session"`
	// Replaced : la séance d'origine, remise en place si la course est retirée.
	Replaced *PlannedSession `bson:"replaced,omitempty" json:"-"`
}

// RaceInfo : ce qu'on sait de l'épreuve visée, quand c'est une course officielle.
// Rempli par une recherche, donc à prendre comme une aide et non une source sûre.
type RaceInfo struct {
	Place string  `bson:"place,omitempty" json:"place,omitempty"`
	Lat   float64 `bson:"lat,omitempty" json:"lat,omitempty"`
	Lon   float64 `bson:"lon,omitempty" json:"lon,omitempty"`
	// ElevationGainM : dénivelé positif annoncé, 0 si inconnu.
	ElevationGainM float64 `bson:"elevation_gain_m,omitempty" json:"elevation_gain_m,omitempty"`
	// Course : le parcours en quelques phrases (profil, revêtement, passages durs).
	Course string `bson:"course,omitempty" json:"course,omitempty"`
	URL    string `bson:"url,omitempty" json:"url,omitempty"`
	// Climate : météo habituelle à cette date et cet endroit (moyennes des années passées).
	Climate   *RaceClimate `bson:"climate,omitempty" json:"climate,omitempty"`
	UpdatedAt time.Time    `bson:"updated_at,omitempty" json:"updated_at,omitempty"`
}

// RaceClimate : températures et pluie habituelles le jour de la course.
type RaceClimate struct {
	TempMinC float64 `bson:"temp_min_c" json:"temp_min_c"`
	TempMaxC float64 `bson:"temp_max_c" json:"temp_max_c"`
	// RainyDaysPct : part des années passées où il a plu ce jour-là (0–100).
	RainyDaysPct float64 `bson:"rainy_days_pct" json:"rainy_days_pct"`
	Years        int     `bson:"years" json:"years"`
}

// SessionOverride fixe le sort d'une séance précise, décidé avec le coach : date
// imposée (report) ou annulation. Prime sur le motif hebdomadaire et sur le
// décalage automatique lié à une indisponibilité.
type SessionOverride struct {
	Week    int `bson:"week" json:"week"`
	Session int `bson:"session" json:"session"`
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
	ID            primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID        primitive.ObjectID `bson:"user_id" json:"-"` // jamais exposé au client
	DistanceKm    float64            `bson:"distance_km" json:"distance_km"`
	DistanceLabel string             `bson:"distance_label" json:"distance_label"`
	// Terrain : "road" ou "trail". Absent sur les objectifs créés avant ce champ —
	// passer par EffectiveTerrain plutôt que de lire la valeur brute.
	Terrain string `bson:"terrain,omitempty" json:"terrain,omitempty"`
	// RaceDate : jour de la course au format AAAA-MM-JJ, quand la personne l'a indiqué
	// plutôt qu'un nombre de semaines. C'est la vraie échéance : Weeks n'en est qu'une
	// approximation arrondie à la semaine. Vide = échéance déduite de Weeks.
	RaceDate string `bson:"race_date,omitempty" json:"race_date,omitempty"`
	// CustomTitle : libellé choisi par l’utilisateur (affiché à la place de distance_label si non vide).
	CustomTitle     string           `bson:"custom_title,omitempty" json:"custom_title,omitempty"`
	Weeks           int              `bson:"weeks" json:"weeks"`
	SessionsPerWeek int              `bson:"sessions_per_week" json:"sessions_per_week"`
	TargetTime      string           `bson:"target_time,omitempty" json:"target_time"`
	Plan            string           `bson:"plan" json:"plan"`
	PlannedSessions []PlannedSession `bson:"planned_sessions,omitempty" json:"planned_sessions,omitempty"`
	// CalendarDayOffsets : jours 0=lundi…6=dimanche ; si absent ou longueur ≠ sessions_per_week, on utilise le motif par défaut.
	CalendarDayOffsets []int `bson:"calendar_day_offsets,omitempty" json:"calendar_day_offsets,omitempty"`
	// SessionOverrides : reports et annulations séance par séance décidés avec le coach.
	SessionOverrides []SessionOverride `bson:"session_overrides,omitempty" json:"session_overrides,omitempty"`
	// Unavailabilities : périodes sans course ; les séances concernées sont décalées.
	Unavailabilities []Unavailability `bson:"unavailabilities,omitempty" json:"unavailabilities,omitempty"`
	// Races : courses prévues pendant la préparation, chacune à la place d'une séance.
	Races []GoalRace `bson:"races,omitempty" json:"races,omitempty"`
	// RaceName : nom de l'épreuve visée (« Marathon de Paris »), si la personne l'a donné.
	RaceName string `bson:"race_name,omitempty" json:"race_name,omitempty"`
	// RaceOfficial : vraie course avec dossard ; faux = défi personnel.
	RaceOfficial bool      `bson:"race_official,omitempty" json:"race_official,omitempty"`
	RaceInfo     *RaceInfo `bson:"race_info,omitempty" json:"race_info,omitempty"`
	// CoachNotes : ce que le coach a retenu de la personne au fil des échanges
	// (blessure ancienne, préférence d'horaire…). Relu à chaque conversation.
	CoachNotes  []string   `bson:"coach_notes,omitempty" json:"coach_notes,omitempty"`
	CoachThread []ChatTurn `bson:"coach_thread,omitempty" json:"coach_thread,omitempty"`
	// Insight : dernière synthèse du coach sur l'avancement, gardée tant que rien ne change.
	Insight *GoalInsight `bson:"insight,omitempty" json:"-"`
	// LevelHistory : niveau estimé sur la distance, un point par semaine de préparation.
	// C'est lui qui dit si l'on progresse, l'assiduité ne disant que si l'on obéit.
	LevelHistory []GoalLevelPoint `bson:"level_history,omitempty" json:"-"`
	// Result : le chrono réellement couru le jour de la course.
	Result *GoalResult `bson:"result,omitempty" json:"result,omitempty"`
	// PlanWithoutStravaData : plan généré sans JSON d’activités (objectif seul) ; repasse à false après régénération avec Strava.
	PlanWithoutStravaData bool      `bson:"plan_without_strava_data,omitempty" json:"plan_without_strava_data,omitempty"`
	CreatedAt             time.Time `bson:"created_at" json:"created_at"`
}

// EffectiveTerrain : les objectifs antérieurs à ce champ décrivent tous une course
// sur route, et toute valeur inattendue retombe sur ce même défaut.
func (g *Goal) EffectiveTerrain() string {
	if g.Terrain == GoalTerrainTrail {
		return GoalTerrainTrail
	}
	return GoalTerrainRoad
}

// GoalLevelPoint : le niveau estimé un jour donné, en fourchette (voir l'avis de
// faisabilité) : FastSec suppose l'endurance au point, SlowSec est ce que les
// sorties garantissent.
type GoalLevelPoint struct {
	Date         string  `bson:"date" json:"date"`
	FastSec      float64 `bson:"fast_sec" json:"fast_sec"`
	SlowSec      float64 `bson:"slow_sec" json:"slow_sec"`
	PredictedSec float64 `bson:"predicted_sec" json:"predicted_sec"`
	Reliability  string  `bson:"reliability,omitempty" json:"reliability,omitempty"`
	// ReliabilityWhy : la donnée qui limite l'estimation, dite en mots.
	ReliabilityWhy string `bson:"reliability_why,omitempty" json:"reliability_why,omitempty"`
}

// Origines d'un résultat de course.
const (
	GoalResultStrava   = "strava"
	GoalResultNeuroRun = "neurorun"
	GoalResultManual   = "manual"
)

// GoalResult : le chrono couru le jour J, face au chrono visé.
type GoalResult struct {
	TimeSec    float64 `bson:"time_sec" json:"time_sec"`
	DistanceKm float64 `bson:"distance_km,omitempty" json:"distance_km,omitempty"`
	// Source : strava, neurorun ou manual (saisi à la main).
	Source           string    `bson:"source" json:"source"`
	StravaActivityID int64     `bson:"strava_activity_id,omitempty" json:"strava_activity_id,omitempty"`
	LiveRunID        string    `bson:"live_run_id,omitempty" json:"live_run_id,omitempty"`
	RecordedAt       time.Time `bson:"recorded_at" json:"recorded_at"`
}

// GoalInsight : la synthèse du coach, avec l'empreinte de l'état qu'elle commente.
// Tant que l'empreinte est la même, on la ressert au lieu de rappeler le modèle.
type GoalInsight struct {
	Key       string    `bson:"key" json:"-"`
	Text      string    `bson:"text" json:"text"`
	Tone      string    `bson:"tone" json:"tone"` // good | watch | alert
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}
