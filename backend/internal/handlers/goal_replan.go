package handlers

import (
	"context"
	"errors"
	"fmt"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
)

// synthesizeTrainingPlan rédige le plan. `onProgress` est facultatif : quand il est
// fourni, la réponse du modèle est diffusée au fil de l'eau et l'avancement est
// mesuré sur ce qui est réellement écrit (voir planProgress).
func (h *Handlers) synthesizeTrainingPlan(
	ctx context.Context,
	actsJSON []byte,
	label, targetTime string,
	weeks, spw int,
	hasStravaData bool,
	onProgress func(planProgress),
) (string, []models.PlannedSession, error) {
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

	var plan string
	var err error
	if onProgress == nil {
		plan, err = h.openai.Chat(ctx, system, userQ)
	} else {
		plan, err = h.streamPlan(ctx, system, userQ, weeks, onProgress)
	}
	if err != nil {
		return "", nil, err
	}

	if onProgress != nil {
		onProgress(planProgress{Done: planWritingUnits(weeks), Total: planTotalUnits(weeks), Label: "Découpage des séances"})
	}
	planned, exErr := goalcalendar.ExtractPlannedSessions(ctx, h.openai, plan, weeks, spw)
	if exErr != nil || len(planned) == 0 {
		planned = goalcalendar.FallbackPlannedSessionsFromPlan(plan, weeks, spw)
	}
	if onProgress != nil {
		onProgress(planProgress{
			Done:  planWritingUnits(weeks) + 1,
			Total: planTotalUnits(weeks),
			Label: fmt.Sprintf("%d séances repérées", len(planned)),
		})
	}
	return plan, planned, nil
}
