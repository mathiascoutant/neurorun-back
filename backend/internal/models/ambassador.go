package models

import (
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Étapes de vie d’un contrat d’ambassadeur.
const (
	// AmbassadorStatusPending : l’ambassadeur est désigné, son contrat l’attend.
	AmbassadorStatusPending = "pending"
	// AmbassadorStatusSigned : l’ambassadeur a signé, NeuroRun doit contresigner.
	AmbassadorStatusSigned = "signed"
	// AmbassadorStatusActive : les deux signatures sont là, le contrat court.
	AmbassadorStatusActive = "active"
	// AmbassadorStatusEnded : contrat terminé (préavis ou manquement). Le code est désactivé.
	AmbassadorStatusEnded = "ended"
)

// AmbassadorContractVersion : version du texte signé. À incrémenter dès que le contrat
// change, pour qu’on sache toujours quel texte chaque ambassadeur a accepté.
const AmbassadorContractVersion = "2026-10"

// AmbassadorMention : mention manuscrite exigée au-dessus de la signature.
const AmbassadorMention = "Lu et approuvé"

// Signature : qui a signé, quoi, quand. L’IP et le navigateur sont conservés comme
// faisceau de preuves d’une signature électronique simple.
type Signature struct {
	// FullName : nom saisi par le signataire, qui vaut signature.
	FullName string `bson:"full_name" json:"full_name"`
	// Mention : « Lu et approuvé », recopiée par le signataire.
	Mention  string    `bson:"mention" json:"mention"`
	SignedAt time.Time `bson:"signed_at" json:"signed_at"`
	IP       string    `bson:"ip,omitempty" json:"-"`
	Agent    string    `bson:"agent,omitempty" json:"-"`
}

// Ambassador : un compte NeuroRun désigné ambassadeur, son contrat et son code.
//
// Email, prénom et nom du compte sont recopiés ici pour que la console admin affiche la
// liste sans relire la collection des comptes à chaque ligne ; l’identité qui engage,
// elle, est celle que l’ambassadeur saisit en signant (ContractLastName / FirstName).
type Ambassador struct {
	ID     primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	UserID primitive.ObjectID `bson:"user_id" json:"user_id"`
	Email  string             `bson:"email" json:"email"`
	// Identité du contrat, saisie par l’ambassadeur au moment de signer.
	ContractLastName  string `bson:"contract_last_name,omitempty" json:"contract_last_name,omitempty"`
	ContractFirstName string `bson:"contract_first_name,omitempty" json:"contract_first_name,omitempty"`
	// Instagram : compte sur lequel il publie, sans arobase.
	Instagram string `bson:"instagram,omitempty" json:"instagram,omitempty"`
	// PromoCodeID : code de réduction personnel, créé avec l’ambassadeur.
	PromoCodeID *primitive.ObjectID `bson:"promo_code_id,omitempty" json:"promo_code_id,omitempty"`
	Status      string              `bson:"status" json:"status"`
	// ContractVersion : version du texte au moment de la signature de l’ambassadeur.
	ContractVersion     string     `bson:"contract_version,omitempty" json:"contract_version,omitempty"`
	AmbassadorSignature *Signature `bson:"ambassador_signature,omitempty" json:"ambassador_signature,omitempty"`
	CompanySignature    *Signature `bson:"company_signature,omitempty" json:"company_signature,omitempty"`
	// Notes : usage interne (console admin), jamais renvoyé à l’ambassadeur.
	Notes     string     `bson:"notes,omitempty" json:"notes,omitempty"`
	CreatedAt time.Time  `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time  `bson:"updated_at" json:"updated_at"`
	EndedAt   *time.Time `bson:"ended_at,omitempty" json:"ended_at,omitempty"`
}

// EffectiveStatus : statut déduit des signatures, pour ne jamais afficher « en attente »
// sur un contrat déjà signé des deux côtés si le champ est resté en arrière.
func (a *Ambassador) EffectiveStatus() string {
	if a == nil {
		return ""
	}
	if a.Status == AmbassadorStatusEnded {
		return AmbassadorStatusEnded
	}
	return a.SignatureStatus()
}

// SignatureStatus : où en est le contrat au vu des seules signatures, sans tenir compte
// d’une fin de contrat. C’est le statut auquel on revient quand on reprend un partenariat.
func (a *Ambassador) SignatureStatus() string {
	switch {
	case a == nil:
		return AmbassadorStatusPending
	case a.AmbassadorSignature != nil && a.CompanySignature != nil:
		return AmbassadorStatusActive
	case a.AmbassadorSignature != nil:
		return AmbassadorStatusSigned
	default:
		return AmbassadorStatusPending
	}
}

// ContractDisplayName : « Gaël BIZOT », tel qu’il apparaît en tête du bloc de signature.
func (a *Ambassador) ContractDisplayName() string {
	if a == nil {
		return ""
	}
	return DisplayName(a.ContractFirstName, a.ContractLastName, a.Email)
}

// NormalizeInstagram : accepte « @compte », « compte » ou une URL, et garde le pseudo seul.
func NormalizeInstagram(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "www.")
	s = strings.TrimPrefix(s, "instagram.com/")
	s = strings.TrimPrefix(s, "@")
	if i := strings.IndexAny(s, "/?"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// MentionMatches : comparaison tolérante de la mention recopiée (casse, accents, ponctuation).
func MentionMatches(raw string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		repl := map[rune]rune{'é': 'e', 'è': 'e', 'ê': 'e', 'à': 'a', 'â': 'a', 'î': 'i', 'ô': 'o', 'û': 'u', 'ù': 'u', 'ç': 'c'}
		var b strings.Builder
		for _, r := range s {
			if sub, ok := repl[r]; ok {
				b.WriteRune(sub)
				continue
			}
			if r == ' ' || (r >= 'a' && r <= 'z') {
				b.WriteRune(r)
			}
		}
		return strings.Join(strings.Fields(b.String()), " ")
	}
	return norm(raw) == norm(AmbassadorMention)
}
