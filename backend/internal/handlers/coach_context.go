package handlers

import (
	"fmt"
	"math"
	"strings"
	"time"

	"runapp/internal/goalcalendar"
	"runapp/internal/shoes"
)

// coachCareGuidelines : le ton commun aux deux coachs (chat libre et coach d'objectif).
// Ils doivent donner envie de continuer et veiller sur la personne, pas seulement
// répondre à la question posée.
const coachCareGuidelines = `**Ton rôle de coach**
- Tu es un vrai coach qui suit la personne au quotidien : chaleureux·se, encourageant·e, concret·e. Salue les efforts et les progrès réels (séance faite, régularité, record), avec des chiffres quand tu en as, sans flatterie creuse.
- Quand la motivation baisse, une séance est ratée ou la forme n'est pas là : déculpabilise, rappelle le chemin déjà fait et propose une prochaine étape simple et atteignable.
- Prends soin de la personne : sommeil, fatigue, stress, douleurs, récupération, hydratation. Une douleur qui dure ou qui augmente → repos et avis d'un·e professionnel·le de santé ; tu ne diagnostiques jamais.
- Termine souvent par une question ou un petit encouragement tourné vers la prochaine sortie, sans le faire à chaque message.`

// shoeStatusLabels : l'état d'usure dit en mots, avec la conduite à tenir pour le coach.
var shoeStatusLabels = map[string]string{
	shoes.StatusNew:     "quasi neuve",
	shoes.StatusGood:    "en bon état",
	shoes.StatusWatch:   "à surveiller : l'amorti commence à baisser",
	shoes.StatusReplace: "à remplacer bientôt : à garder pour des sorties tranquilles",
	shoes.StatusWorn:    "USÉE : durée de vie théorique dépassée, risque de blessure",
}

func shoeCategoryLabel(id string) string {
	for _, c := range shoeCategoryLabels {
		if c.ID == id {
			return c.Label
		}
	}
	return "Polyvalente"
}

func shoeDisplayName(v shoeView) string {
	name := strings.TrimSpace(v.Brand + " " + v.Model)
	if nick := strings.TrimSpace(v.Nickname); nick != "" {
		name += " « " + nick + " »"
	}
	return name
}

// shoesCoachBlock décrit au coach les paires de la personne et leur usure. Les
// chiffres viennent du serveur : le modèle n'a rien à recalculer, seulement à
// en tirer un conseil au bon moment.
func shoesCoachBlock(views []shoeView, loc *time.Location) string {
	var b strings.Builder
	b.WriteString("**Chaussures de la personne**\n")

	active := activeShoes(views)
	if len(active) == 0 {
		b.WriteString("- Aucune paire suivie dans NeuroRun. Si la personne parle de ses chaussures ou d'une douleur, tu peux suggérer de les ajouter dans l'onglet Chaussures pour suivre leur usure, sans insister.\n")
		return b.String()
	}
	for _, v := range active {
		w := v.Wear
		line := fmt.Sprintf("- %s (%s) : %.0f km sur %.0f km de durée de vie, soit %.0f %% — %s",
			shoeDisplayName(v), shoeCategoryLabel(v.Category),
			w.UsedKm, w.LifespanKm, math.Round(w.WearPct), shoeStatusLabels[w.Status])
		if w.Status != shoes.StatusWorn {
			line += fmt.Sprintf(" ; encore ≈ %.0f km", w.RemainingKm)
		}
		if w.KmPerWeek > 0 {
			line += fmt.Sprintf(" ; %.0f km/semaine", w.KmPerWeek)
		}
		if w.ReplaceOn != "" {
			if day, ok := goalcalendar.ParseDateKey(w.ReplaceOn, loc); ok {
				line += fmt.Sprintf(" ; à changer vers le %s", day.Format("02/01/2006"))
			}
		}
		if v.Runs > 0 {
			line += fmt.Sprintf(" ; %d course(s) avec", v.Runs)
		}
		b.WriteString(line + "\n")
	}
	if retired := len(views) - len(active); retired > 0 {
		fmt.Fprintf(&b, "- %d paire(s) mise(s) au placard, à ne plus conseiller.\n", retired)
	}
	b.WriteString(`Règles pour les chaussures :
- Paire USÉE : préviens clairement (amorti tassé, risque de blessure) et conseille de la remplacer ; propose une autre paire de la liste en attendant.
- Paire à remplacer bientôt ou à surveiller : glisse-le quand une séance exigeante ou longue arrive, et propose de la réserver aux sorties faciles.
- Associe la paire au contenu exact de la séance, pas au type de l'objectif : trail pour le dénivelé et les sentiers, compétition ou dynamique pour le fractionné et les allures rapides, polyvalente ou amorti pour l'endurance et les sorties sur route. Ne conseille pas une paire de compétition pour toutes les sorties.
- Si aucune paire adaptée n'est en état (par exemple la seule paire de route est usée), dis-le franchement et propose la moins mauvaise option en expliquant le compromis.
- Douleur (genou, tibia, mollet, pied) : regarde aussi l'usure de la paire portée.
- Ne répète pas le même avertissement à chaque message : une fois suffit, sauf si la personne en parle ou qu'une séance le justifie.
- Cite les paires par leur nom, et uniquement celles de cette liste.
`)
	return b.String()
}
