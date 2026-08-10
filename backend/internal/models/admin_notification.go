package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Types d’évènements notifiés aux administrateurs.
const (
	// AdminEventSignup : création de compte (offre gratuite ou payante dès l’inscription).
	AdminEventSignup = "signup"
	// AdminEventPlanActivated : passage à une offre payante après création du compte.
	// C’est le cas nominal côté web : /auth/register crée le compte en standard, puis Stripe active l’offre.
	AdminEventPlanActivated = "plan_activated"
)

// AdminNotification : évènement destiné aux administrateurs. Historique consultable dans
// l’app (onglet Admin → Activité) en plus de l’envoi push.
type AdminNotification struct {
	ID   primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Kind string             `bson:"kind" json:"kind"`
	// Title / Body : texte exact affiché dans la notification push et dans la liste.
	Title string `bson:"title" json:"title"`
	Body  string `bson:"body" json:"body"`
	// UserID : compte concerné par l’évènement (pas le destinataire).
	UserID    primitive.ObjectID `bson:"user_id" json:"user_id"`
	UserEmail string             `bson:"user_email" json:"user_email"`
	UserName  string             `bson:"user_name,omitempty" json:"user_name,omitempty"`
	Plan      string             `bson:"plan" json:"plan"`
	PlanLabel string             `bson:"plan_label,omitempty" json:"plan_label,omitempty"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
	// ReadAt : marquage global (un seul admin en pratique) via POST /api/admin/notifications/read.
	ReadAt *time.Time `bson:"read_at,omitempty" json:"read_at,omitempty"`
}

// AdminPushToken : jeton Expo d’un appareil connecté sur un compte admin. Supprimé à la
// déconnexion, à la perte du rôle admin, ou quand Expo répond DeviceNotRegistered.
type AdminPushToken struct {
	ID    primitive.ObjectID `bson:"_id,omitempty" json:"-"`
	Token string             `bson:"token" json:"token"`
	// UserID : dernier compte admin ayant enregistré ce jeton (un appareil = un compte).
	UserID    primitive.ObjectID `bson:"user_id" json:"-"`
	Platform  string             `bson:"platform,omitempty" json:"platform,omitempty"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time          `bson:"updated_at" json:"updated_at"`
}
