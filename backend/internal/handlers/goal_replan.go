package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
)

// goalAdjustIntent : sortie JSON de l’extracteur + heuristique.
type goalAdjustIntent struct {
	Replan          bool    `json:"replan"`
	SessionsPerWeek *int    `json:"sessions_per_week"`
	Weeks           *int    `json:"weeks"`
	TargetTime      *string `json:"target_time"`
	AvoidWednesday  bool    `json:"avoid_wednesday"`
}

var (
	reTwoSessions   = regexp.MustCompile(`(?i)\b(2|deux)\s*s[ée]ances?\b`)
	reThreeSessions = regexp.MustCompile(`(?i)\b(3|trois)\s*s[ée]ances?\b`)
)

func (h *Handlers) synthesizeTrainingPlan(ctx context.Context, actsJSON []byte, label, targetTime string, weeks, spw int, hasStravaData bool) (string, []models.PlannedSession, error) {
	if weeks < 1 || spw < 1 || weeks > 52 || spw > 7 {
		return "", nil, errors.New("bad weeks/spw")
	}
	var system string
	if hasStravaData {
		system = `Tu es un coach course à pied. Tu écris en français, en TUTOIEMENT. Style clair : phrases courtes, listes à puces, peu de blocs denses. Niveau accessible : une seule fois, rappelle que min/km = minutes pour parcourir 1 km.

Tu reçois des activités Strava en JSON + un objectif (distance, chrono, semaines restantes, séances/semaine).

**Impératif chiffres** : le plan doit être **exécutable sans deviner**. Dès que tu donnes une séance, indique des **allures en min/km** (fourchettes courtes, ex. 5:35–5:50) et, pour tout fractionné, le **temps cible par répétition** (ex. 800 m viser ~2:40–2:55). Si un chrono est chiffré pour la course (ex. 50 min sur 10 km), calcule l’**allure course cible** (temps total en minutes ÷ distance en km) et déduis des repères cohérents : facile/endurance un peu plus lent que la course, seuil/tempo entre les deux, fractions un peu plus vite que l’allure course — adapte à ce que montrent les données JSON.

Rédige un plan en Markdown avec EXACTEMENT ces sections (titres ## comme ci-dessous), dans l'ordre :

## Rappel faisabilité
L'utilisateur a déjà lu un avis détaillé. 2 phrases maximum : à quel point le chrono est cohérent avec ses sorties. Pas de répétition longue.

## Où tu en es aujourd'hui
4 à 6 puces max, uniquement à partir du JSON (allures, volume, régularité). Formulations simples.

## Les 3 idées à retenir
Exactement 3 puces courtes : ce qui va t'aider à progresser sans te blesser.

## Repères d'allure pour cette prépa
5 à 8 puces **avec chiffres** : **allure course (objectif)** en min/km ; **allure facile / endurance** ; **allure seuil ou tempo ou « un peu sous allure course »** ; pour au moins deux distances de fraction courantes (ex. 400 m et 800 m ou 1 km), donne un **temps cible par répétition** cohérent avec l’objectif. Une ligne peut rappeler l’échauffement **10–15 min** ou **1,5–2 km** à allure facile. Pas de jargon non chiffré du type « allure 10 km » sans min/km à côté.

## Calendrier — semaine par semaine
Pour chaque semaine, utilise un sous-titre ### Semaine 1, ### Semaine 2, etc. (autant que les semaines disponibles jusqu'à la course).
Pour **chaque** séance (Séance 1, 2, …) une **seule puce** ou une **liste courte** qui précise **dans cet ordre** : (1) échauffement avec durée ou km + allure facile en min/km ; (2) corps de séance avec volumes, **nombre de répétitions**, et pour chaque type de rep **temps visé** ou **allure min/km** ; (3) retour au calme (km ou min + allure). Évite les formulations vagues (« tranquille », « un peu vite ») sans fourchette min/km. Respecte le nombre de séances par semaine demandé.

## Dans les derniers jours avant la course
2 à 4 puces : repos, dernier petit effort **avec durée et allure facile** si tu en proposes un, pas de gros volume.

## Sécurité
2 puces : douleur anormale = arrêt et avis médical ; hydratation et écoute du corps.

## Échanges avec le coach
2 puces courtes : invite à utiliser le fil de discussion sous cet objectif pour dire comment tu te sens (forme, sommeil, stress), parler de gênes ou douleurs, et ajuster ensemble le rythme ou le chrono si besoin — sans jugement.

Pas de paragraphes de plus de 3 phrases d'affilée. Pas de listes numérotées longues.

**Activités (JSON) :** ` + string(actsJSON)
	} else {
		system = `Tu es un coach course à pied. Tu écris en français, en TUTOIEMENT. Style clair : phrases courtes, listes à puces. Niveau accessible : une fois, rappelle que min/km = minutes pour parcourir 1 km.

**Contexte** : tu n’as **pas** d’historique d’activités importé (pas de JSON). Tu construis le plan **uniquement** à partir de l’objectif déclaré (distance, chrono ou intention, délai, séances/semaine). Reste **prudent** : repères génériques, pas de chiffres inventés comme s’ils venaient de sorties réelles. Indique une fois qu’associer Strava permettra d’affiner avec le volume et l’allure réels.

**Impératif chiffres** : le plan doit être **exécutable**. Allures en min/km, temps par répétition si fractionné. Si le chrono est chiffré, calcule l’**allure course cible** (minutes totales ÷ km) et déduis facile / seuil / fractions de façon cohérente.

Rédige un plan en Markdown avec EXACTEMENT ces sections (titres ##), dans l’ordre :

## Rappel faisabilité
2 phrases : sans historique, tu raisonnes sur l’objectif déclaré et tu restes honnête sur l’incertitude.

## Où tu pars (sans historique importé)
3 à 5 puces : expliquer qu’on part de l’intention déclarée ; inviter à noter ressenti et allure ressentie sur les prochaines sorties ; mentionner qu’une liaison Strava aidera à calibrer.

## Les 3 idées à retenir
Exactement 3 puces courtes.

## Repères d'allure pour cette prépa
5 à 8 puces **avec chiffres** (min/km, temps par répétition) dérivés **de l’objectif**, pas d’un historique fictif.

## Calendrier — semaine par semaine
### Semaine 1, ### Semaine 2, etc. Pour chaque séance : échauffement (durée/km + allure), corps, retour au calme — comme pour le mode Strava.

## Dans les derniers jours avant la course
2 à 4 puces.

## Sécurité
2 puces.

## Échanges avec le coach
2 puces : fil sous l’objectif ; lier Strava optionnel pour affiner.

Pas de paragraphes trop longs.

**Note** : pas de données d’activités JSON — n’invente pas de stats personnelles.`
	}

	userQ := fmt.Sprintf(`Objectif course : %s.
Chrono ou intention : %s.
Échéance dans %d semaine(s).
Disponibilité : %d séance(s) par semaine en moyenne (toutes les séances de chaque semaine doivent être décrites — pas de jour inventé côté appli : seuls le nombre et le contenu comptent).
Rédige le plan complet en respectant les titres et le style demandés. Pas de vouvoiement.
Chaque semaine, chaque séance doit contenir des min/km et, si fractionné, des temps par répétition.`, label, targetTime, weeks, spw)

	plan, err := h.openai.Chat(ctx, system, userQ)
	if err != nil {
		return "", nil, err
	}
	planned, exErr := goalcalendar.ExtractPlannedSessions(ctx, h.openai, plan, weeks, spw)
	if exErr != nil || len(planned) == 0 {
		planned = goalcalendar.FallbackPlannedSessionsFromPlan(plan, weeks, spw)
	}
	return plan, planned, nil
}

func (h *Handlers) extractGoalAdjustIntent(ctx context.Context, userMsg string, g *models.Goal) goalAdjustIntent {
	zero := goalAdjustIntent{}
	if g == nil || strings.TrimSpace(h.cfg.OpenAIAPIKey) == "" {
		return zero
	}
	msg := strings.TrimSpace(userMsg)
	if msg == "" {
		return zero
	}
	system := `Tu analyses un message d'un coureur sur SON objectif d'entraînement déjà enregistré dans une appli.

Réponds UNIQUEMENT un objet JSON valide (pas de markdown, pas de texte autour), avec exactement ces clés :
{"replan": boolean, "sessions_per_week": null ou entier 1-7, "weeks": null ou entier 1-52, "target_time": null ou string courte (ex. "50 min"), "avoid_wednesday": boolean}

Règles :
- "replan" true si la personne demande de MODIFIER le plan structuré : nombre de séances par semaine, retirer / éviter un jour (ex. mercredi), changer le calendrier, alléger ou réécrire les séances, ajuster le chrono ou la durée de prépa.
- "replan" false pour simples questions, remerciements, nouvelles sans demande de changement, ou discussion ressenti sans demande d'ajustement du plan.
- "sessions_per_week" : nouvelle valeur UNIQUEMENT si la personne la demande clairement (ex. "passe à 2 séances"). Sinon null.
- "weeks" : idem si elle demande plus ou moins de semaines. Sinon null.
- "target_time" : nouveau chrono UNIQUEMENT si demandé explicitement. Sinon null.
- "avoid_wednesday" true si elle veut garder le même nombre de séances mais SANS le mercredi (ou "enlève le mercredi") ; si elle demande seulement 2 séances par semaine, mets plutôt sessions_per_week à 2 et avoid_wednesday false.

Contexte objectif actuel : ` + fmt.Sprintf("%d séances/sem, %d semaines, chrono %q, distance %s",
		g.SessionsPerWeek, g.Weeks, g.TargetTime, g.DistanceLabel)

	userQ := "Message utilisateur :\n" + msg
	raw, err := h.openai.Chat(ctx, system, userQ)
	if err != nil {
		return zero
	}
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "{"); i >= 0 {
		raw = raw[i:]
	}
	if j := strings.LastIndex(raw, "}"); j >= 0 {
		raw = raw[:j+1]
	}
	var out goalAdjustIntent
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return zero
	}
	return out
}

func heuristicGoalAdjust(msg string, _ *models.Goal) goalAdjustIntent {
	var out goalAdjustIntent
	low := strings.ToLower(strings.TrimSpace(msg))
	if low == "" {
		return out
	}
	if reTwoSessions.MatchString(low) {
		out.Replan = true
		v := 2
		out.SessionsPerWeek = &v
	} else if reThreeSessions.MatchString(low) {
		out.Replan = true
		v := 3
		out.SessionsPerWeek = &v
	}
	if strings.Contains(low, "mercredi") {
		if strings.Contains(low, "enlè") || strings.Contains(low, "enle") ||
			strings.Contains(low, "sans ") || strings.Contains(low, "retir") ||
			strings.Contains(low, "supprim") || strings.Contains(low, "ôte") ||
			strings.Contains(low, "ote ") || strings.Contains(low, "plus de mercredi") {
			out.Replan = true
			out.AvoidWednesday = true
		}
	}
	if strings.Contains(low, "calendrier") && (strings.Contains(low, "adapt") || strings.Contains(low, "modif") ||
		strings.Contains(low, "réaj") || strings.Contains(low, "reaj") || strings.Contains(low, "mettre à jour")) {
		out.Replan = true
	}
	if strings.Contains(low, "séance") && (strings.Contains(low, "semaine") || strings.Contains(low, "/sem")) {
		if strings.Contains(low, "3") || strings.Contains(low, "trois") {
			if !reTwoSessions.MatchString(low) {
				out.Replan = true
				v := 3
				out.SessionsPerWeek = &v
			}
		} else if strings.Contains(low, "2") || strings.Contains(low, "deux") {
			out.Replan = true
			v := 2
			out.SessionsPerWeek = &v
		}
	}
	return out
}

func mergeGoalAdjustIntent(ai, heur goalAdjustIntent) goalAdjustIntent {
	out := ai
	if heur.Replan {
		out.Replan = true
	}
	if heur.SessionsPerWeek != nil {
		out.SessionsPerWeek = heur.SessionsPerWeek
	}
	if heur.Weeks != nil {
		out.Weeks = heur.Weeks
	}
	if heur.TargetTime != nil {
		out.TargetTime = heur.TargetTime
	}
	if heur.AvoidWednesday {
		out.AvoidWednesday = true
	}
	return out
}

func mergedGoalParams(g *models.Goal, intent goalAdjustIntent) (spw, weeks int, target string) {
	spw = g.SessionsPerWeek
	weeks = g.Weeks
	target = strings.TrimSpace(g.TargetTime)
	if intent.SessionsPerWeek != nil {
		v := *intent.SessionsPerWeek
		if v >= 1 && v <= 7 {
			spw = v
		}
	}
	if intent.Weeks != nil {
		v := *intent.Weeks
		if v >= 1 && v <= 52 {
			weeks = v
		}
	}
	if intent.TargetTime != nil {
		t := strings.TrimSpace(*intent.TargetTime)
		if utf8.RuneCountInString(t) >= 2 && utf8.RuneCountInString(t) <= 120 {
			target = t
		}
	}
	return
}

func calendarOffsetsFor(spw int, avoidWednesday bool) []int {
	if spw == 3 && avoidWednesday {
		return []int{0, 3, 4}
	}
	return nil
}

func structuralCalendarChange(g *models.Goal, newOff []int) bool {
	a := g.CalendarDayOffsets
	b := newOff
	if len(a) == 0 && len(b) == 0 {
		return false
	}
	if len(a) != len(b) {
		return true
	}
	return !slices.Equal(a, b)
}

func needsPersistedReplan(g *models.Goal, intent goalAdjustIntent, spw, weeks int, target string, calOff []int) bool {
	if !intent.Replan {
		return false
	}
	if spw != g.SessionsPerWeek || weeks != g.Weeks || target != strings.TrimSpace(g.TargetTime) {
		return true
	}
	return structuralCalendarChange(g, calOff)
}
