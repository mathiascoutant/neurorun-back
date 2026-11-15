package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"runapp/internal/goalcalendar"
	oai "runapp/internal/llm"
	"runapp/internal/models"
	"runapp/internal/store"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	goalCoachMaxTurns = 20
	// goalCoachMaxToolRounds borne l'enchaînement d'actions sur un seul message :
	// le coach doit finir par répondre, pas boucler sur le calendrier.
	goalCoachMaxToolRounds  = 5
	goalCoachPlanContextMax = 3200
)

type goalChatBody struct {
	Message string `json:"message"`
}

func (h *Handlers) GoalChat(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "goals") {
		return
	}

	gid, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}

	g, err := h.db.GetGoalByUser(r.Context(), u.ID, gid)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "erreur"})
		return
	}

	var b goalChatBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	b.Message = strings.TrimSpace(b.Message)
	if b.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message vide"})
		return
	}

	hasStravaData := u.HasStrava()
	actsJSON := []byte("[]")
	if hasStravaData {
		access, err := h.ensureStravaAccess(r.Context(), u)
		if err != nil {
			h.writeStravaError(w, r, u, err, "impossible d'accéder à Strava, reconnectez le compte")
			return
		}
		acts, err := h.strava.ActivitiesSummary(r.Context(), access, 50)
		if err != nil {
			h.writeStravaError(w, r, u, err, "erreur Strava")
			return
		}
		actsJSON, _ = json.Marshal(acts)
	}

	loc, _ := calendarLocation()
	tools := &goalCoachTools{
		h:         h,
		userID:    u.ID,
		goalID:    gid,
		goal:      g,
		loc:       loc,
		now:       time.Now().UTC(),
		actsJSON:  actsJSON,
		hasStrava: hasStravaData,
		user:      u,
		profile:   h.runnerProfileBlock(r.Context(), u, g.DistanceKm),
	}
	// Sans Strava (ou Strava en panne), le coach voit quand même les courses NeuroRun.
	tools.runs, _ = h.loadGoalRuns(r, u, g)
	if views, err := h.loadShoeViews(r.Context(), u.ID); err == nil {
		tools.shoesBlock = shoesCoachBlock(views, loc)
	}

	msgs := []oai.ChatMessage{{Role: "system", Content: tools.systemPrompt(hasStravaData)}}
	hist := g.CoachThread
	if len(hist) > goalCoachMaxTurns {
		hist = hist[len(hist)-goalCoachMaxTurns:]
	}
	msgs = append(msgs, coachHistoryMessages(hist, b.Message, tools.now, loc)...)

	reply, err := h.runGoalCoachTurn(r, msgs, tools)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur IA"})
		return
	}

	if err := h.db.AppendGoalCoachTurns(r.Context(), u.ID, gid, b.Message, reply); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "sauvegarde"})
		return
	}

	actions := dedupActions(tools.actions)
	refreshed, refErr := h.db.GetGoalByUser(r.Context(), u.ID, gid)
	if refErr != nil {
		refreshed = tools.goal
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reply":   reply,
		"goal":    refreshed,
		"actions": actions,
	})
}

// coachHistoryMessages rejoue la discussion en datant chaque journée d'échanges.
// Sans ces repères, le modèle lit « aujourd'hui, mercredi 4 » dans un vieux message
// et s'y fie plutôt qu'à la date du calendrier : il reporte alors la mauvaise
// séance, et la pose dans le passé.
func coachHistoryMessages(hist []models.ChatTurn, message string, now time.Time, loc *time.Location) []oai.ChatMessage {
	var out []oai.ChatMessage
	lastDay := ""
	for _, t := range hist {
		if t.Role != "user" && t.Role != "assistant" {
			continue
		}
		if !t.CreatedAt.IsZero() {
			day := t.CreatedAt.In(loc)
			if key := goalcalendar.DateKey(day); key != lastDay {
				lastDay = key
				out = append(out, oai.ChatMessage{Role: "system", Content: fmt.Sprintf(
					"Échanges du %s %s : dans ces messages, « aujourd'hui », « demain » et « hier » se rapportent à ce jour-là, pas à la date actuelle.",
					weekdayNameOf(day), key)})
			}
		}
		out = append(out, oai.ChatMessage{Role: t.Role, Content: t.Text})
	}
	today := now.In(loc)
	out = append(out,
		oai.ChatMessage{Role: "system", Content: fmt.Sprintf(
			"Nouveau message, envoyé aujourd'hui %s %s : « aujourd'hui » désigne ce jour, « demain » le %s %s.",
			weekdayNameOf(today), goalcalendar.DateKey(today),
			weekdayNameOf(today.AddDate(0, 0, 1)), goalcalendar.DateKey(today.AddDate(0, 0, 1)))},
		oai.ChatMessage{Role: "user", Content: message},
	)
	return out
}

// runGoalCoachTurn laisse le modèle agir avant de répondre : tant qu'il demande des
// actions, elles sont exécutées et leurs retours lui sont renvoyés. Au dernier tour
// les outils sont fermés, pour qu'il conclue par du texte.
func (h *Handlers) runGoalCoachTurn(
	r *http.Request,
	msgs []oai.ChatMessage,
	tools *goalCoachTools,
) (string, error) {
	defs := goalCoachToolDefs()
	for round := 0; ; round++ {
		var assistant oai.ChatMessage
		var err error
		if round >= goalCoachMaxToolRounds {
			assistant, err = h.coachAI.ChatMessagesNoMoreTools(r.Context(), msgs, defs)
		} else {
			assistant, err = h.coachAI.ChatMessagesWithTools(r.Context(), msgs, defs)
		}
		if err != nil {
			if reply := fallbackCoachReply(tools.actions); reply != "" {
				return reply, nil
			}
			return "", err
		}
		if len(assistant.ToolCalls) == 0 {
			reply := strings.TrimSpace(assistant.Content)
			if reply == "" {
				reply = fallbackCoachReply(tools.actions)
			}
			if reply == "" {
				return "", errors.New("réponse vide")
			}
			return reply, nil
		}

		msgs = append(msgs, assistant)
		for _, call := range assistant.ToolCalls {
			msgs = append(msgs, oai.ToolResultMessage(call.ID, tools.execute(r.Context(), call)))
		}
		// Le calendrier vient de changer : le prompt système décrit désormais un
		// planning périmé, et le coach annoncerait de mauvaises dates. On ajoute l'état
		// à jour à la suite plutôt que de réécrire ce prompt : Claude refuse qu'on
		// modifie le début d'une conversation dont il a déjà gardé la réflexion.
		msgs = append(msgs, oai.ChatMessage{Role: "system", Content: tools.refreshNote()})
	}
}

// refreshNote : l'objectif et le calendrier tels qu'ils sont après les actions du
// tour, à lire à la place des sections du même nom du prompt système.
func (t *goalCoachTools) refreshNote() string {
	return "Mise à jour après tes actions — ces informations remplacent « Objectif enregistré » et « Calendrier actuel » du début.\n\n" +
		t.goalSummary() + "\n**Calendrier actuel**\n" + t.scheduleContext()
}

func (t *goalCoachTools) goalSummary() string {
	return `**Objectif enregistré**
- Distance : ` + t.goal.DistanceLabel + `
- Type de course : ` + models.GoalTerrainLabel(t.goal.EffectiveTerrain()) + `
- Chrono visé : ` + t.goal.TargetTime + `
- Délai : ` + strconv.Itoa(t.goal.Weeks) + ` semaine(s)` + raceDateLine(t.goal.RaceDate) + `
- Séances / semaine : ` + strconv.Itoa(t.goal.SessionsPerWeek) + `
`
}

// fallbackCoachReply évite le pire des cas : des modifications enregistrées sans
// que la personne sache lesquelles, parce que la rédaction a échoué après coup.
func fallbackCoachReply(actions []coachAction) string {
	list := dedupActions(actions)
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("C'est enregistré :\n")
	for _, a := range list {
		b.WriteString("- " + a.Label + "\n")
	}
	b.WriteString("\nDis-moi si ça te convient, on peut encore ajuster.")
	return b.String()
}

func (t *goalCoachTools) systemPrompt(hasStravaData bool) string {
	planCtx := t.goal.Plan
	if len(planCtx) > goalCoachPlanContextMax {
		planCtx = planCtx[:goalCoachPlanContextMax] + "\n… (suite du plan omise pour le contexte)"
	}

	activitiesBlock := "**Activités récentes (JSON)**\n" + string(t.actsJSON)
	if !hasStravaData {
		activitiesBlock = "**Historique Strava** : non importé — base tes réponses sur l’objectif et le plan ci-dessus. Tu peux mentionner qu’associer Strava permet d’aligner les conseils sur le volume et l’allure réels, sans insister."
	}

	return `Tu es le coach course à pied personnel de cette personne. Tu la suis sur toute sa préparation : tu connais son objectif, son plan, ce qu'elle a réellement couru et ce qu'elle t'a déjà dit. Tu écris en français.

**Style**
- Tutoiement par défaut ; si la personne vouvoie, vouvoie sans le commenter.
- Chaleureux, direct, concret. Pas de stéréotypes de genre, de corps ou de niveau.
- Tu parles comme un coach qui connaît son coureur : appuie-toi sur ses chiffres (séances faites, allures, notes), pas sur des généralités.

**Comprendre avant d'agir**
- Demande claire et complète (« décale ma séance de jeudi à vendredi », « je ne peux plus courir le lundi ») : agis tout de suite, dans le même tour.
- Demande floue ou qui engage la santé (« je suis malade », « j'ai mal au genou », « je suis crevé », « ça va pas ») : ne modifie RIEN au premier message. Montre que tu as entendu, puis pose 2 ou 3 questions courtes, celles dont la réponse change ta décision :
  - Maladie : depuis quand ? fièvre, toux, symptômes sous le cou (poitrine, courbatures, digestif) ou seulement au-dessus (nez, gorge) ? jusqu'à quand penses-tu lever le pied ?
  - Douleur ou blessure : où exactement ? depuis quand ? pendant la course, après, ou au repos aussi ? ça augmente quand tu cours ? as-tu vu quelqu'un ?
  - Fatigue, moral : sommeil, travail, charge des dernières semaines ? envie de couper ou d'alléger ?
  Termine par ce que tu proposes de faire selon la réponse (« si c'est juste un rhume, je garde tes footings et j'enlève le fractionné de jeudi »).
- Dès que la personne a répondu, DÉCIDE et AGIS avec tes outils, puis explique. Ne repose pas de question si elle a déjà tout dit dans son message. Si elle te dit explicitement quoi faire (« annule mes deux prochaines séances »), fais-le sans interrogatoire.
- Repères de décision : fièvre ou symptômes sous le cou → aucune course tant que ça dure, puis reprise par deux ou trois footings faciles. Douleur qui augmente en courant ou présente au repos → arrêt, avis d'un·e professionnel·le de santé, et séances remplacées ou annulées. Simple fatigue → on allège (footing à la place du fractionné, sortie longue raccourcie) plutôt que d'annuler.
- Ce que la personne t'apprend de durable sur elle (blessure, contrainte, préférence), retiens-le avec remember, sans le lui annoncer comme une formalité.

**Tu agis avec tes outils, tu ne promets rien**
- N'annonce jamais un changement sans l'avoir appliqué. Ne dis « c'est modifié » que si l'outil a répondu ok ; s'il renvoie une erreur, dis-le simplement et propose autre chose.
- Tu peux enchaîner plusieurs outils dans le même tour (annuler deux séances, alléger la suivante).
- Contrainte récurrente sur un jour → move_training_day (ou set_training_days pour rééquilibrer la semaine).
- Empêchement daté (maladie, blessure, déplacement) → add_unavailability sur la période exacte ; les séances concernées sont reportées automatiquement, annonce leurs nouvelles dates telles que l'outil les renvoie.
- Un seul jour à bouger → reschedule_session. Une séance à laisser tomber → skip_session.
- Contenu d'une séance à adapter (« je n'ai qu'une heure pour ma sortie longue », « je préfère un footing à la place du fractionné », « baisse les allures cette semaine ») → edit_session, avec le déroulé complet de la nouvelle séance. C'est l'outil à utiliser pour alléger sans annuler.
- La personne a prévu une course avant son objectif (un 10 km, un semi) → add_race : elle devient une séance de la préparation. Allège ensuite la séance de la veille ou de l'avant-veille avec edit_session si elle est dure, et dis à quoi sert cette course dans la prépa (test d'allure, répétition générale).
- Charge, échéance ou chrono à revoir en profondeur (arrêt long, objectif devenu irréaliste) → update_goal_settings. Tout le plan à réécrire sans changer sa structure → regenerate_plan. Ces deux outils sont lents : réserve-les aux vrais changements de fond, jamais pour une ou deux séances.
- Séance passée ratée : ne touche pas au calendrier si la personne ne le demande pas ; rassure-la, puis propose de la rattraper ou de la laisser.
- « Aujourd'hui », « demain », « ma séance du jour » : calcule-les UNIQUEMENT depuis la date du jour donnée dans « Calendrier actuel », jamais depuis les dates citées dans les anciens messages. « Ma séance d'aujourd'hui » est celle marquée AUJOURD'HUI ; s'il n'y en a pas, dis-le au lieu d'en déplacer une autre.
- Espace les séances : évite deux séances exigeantes deux jours de suite. Si le déplacement demandé crée ce cas, applique-le si la personne y tient, mais dis-le et propose une autre répartition.
- Après une interruption, ne reprends pas la charge d'avant : réduis d'abord, puis remonte.

` + coachCareGuidelines + `

**Forme des réponses**
3 à 8 phrases en général, ou quelques puces courtes. Quand tu as modifié la préparation, dis précisément ce qui a changé, avec les jours et les dates. Quand tu parles d'une séance, décris-la d'après son contenu listé dans « Calendrier actuel », jamais d'après le type de l'objectif. Si tu détailles une séance, donne des allures en min/km (calées sur la VMA mesurée quand elle existe) et des temps par répétition pour du fractionné. Quand la personne parle de sa prochaine séance, dis-lui en une ligne quelle paire mettre si une paire de la liste s'y prête mieux, ou si celle qu'elle porte d'habitude est usée.

` + t.goalSummary() + `
` + t.profile + `
` + raceBlock(t.goal) + `
` + t.notesBlock() + `
**Où en est la préparation**
` + t.progressContext() + `
**Calendrier actuel**
` + t.scheduleContext() + `
**Plan (référence, texte d'origine — une séance marquée « modifiée » dans le calendrier fait foi)**
` + planCtx + `

` + t.shoesBlock + `
` + activitiesBlock
}

// notesBlock : ce que le coach a retenu de la personne, numéroté pour forget.
func (t *goalCoachTools) notesBlock() string {
	if len(t.goal.CoachNotes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("**Ce que tu as retenu de la personne**\n")
	for i, n := range t.goal.CoachNotes {
		fmt.Fprintf(&b, "%d. %s\n", i+1, n)
	}
	return b.String()
}

// progressContext : les chiffres de la préparation et le détail des dernières
// séances échues, pour que le coach parle de ce qui a été couru et non du plan seul.
func (t *goalCoachTools) progressContext() string {
	items := t.runs.items(t.goal, t.loc, t.now)
	p := computeGoalProgress(t.goal, items, t.loc, t.now)
	var b strings.Builder
	if p.Week > 0 {
		fmt.Fprintf(&b, "- Semaine %d sur %d, course dans %d jour(s)\n", p.Week, p.Weeks, p.DaysToRace)
	} else {
		fmt.Fprintf(&b, "- Préparation de %d semaines, course dans %d jour(s)\n", p.Weeks, p.DaysToRace)
	}
	fmt.Fprintf(&b, "- Séances : %d réalisées, %d partielles, %d manquées, %d annulées, %d à venir\n",
		p.Done, p.Partial, p.Missed, p.Skipped, p.Upcoming)
	if p.AdherencePct >= 0 {
		fmt.Fprintf(&b, "- Assiduité : %d %% (manquées sur 14 jours : %d)\n", p.AdherencePct, p.RecentMissed)
	}
	fmt.Fprintf(&b, "- Volume : %s km courus pour %s km prévus à ce jour\n", kmFR(p.KmDone), kmFR(p.KmPlannedSoFar))

	// Les six dernières séances échues, de la plus récente à la plus ancienne.
	var past []goalcalendar.CalendarItem
	for _, it := range items {
		if it.Status == "done" || it.Status == "partial" || it.Status == "missed" {
			past = append(past, it)
		}
	}
	sort.SliceStable(past, func(i, j int) bool { return past[i].Date > past[j].Date })
	if len(past) > 6 {
		past = past[:6]
	}
	if len(past) > 0 {
		b.WriteString("- Dernières séances échues :\n")
	}
	statusFR := map[string]string{"done": "réalisée", "partial": "partielle", "missed": "manquée"}
	for _, it := range past {
		line := fmt.Sprintf("  - %s (S%d séance %d) %s : %s", it.Date, it.Week, it.Session, truncateRunes(it.Summary, 70), statusFR[it.Status])
		if it.ActualKm != nil {
			line += fmt.Sprintf(" — %s km courus pour %s prévus", kmFR(*it.ActualKm), kmFR(it.PlannedKm))
			pace := it.ActualPaceSecPerKm
			if it.EffortPaceSecPerKm != nil {
				pace = it.EffortPaceSecPerKm
			}
			if pace != nil && *pace > 0 {
				line += ", allure " + paceLabel(*pace)
				if it.TargetPaceSecPerKm != nil && *it.TargetPaceSecPerKm > 0 {
					line += " (cible " + paceLabel(*it.TargetPaceSecPerKm) + ")"
				}
			}
		}
		if it.ScoreTotal != nil {
			line += fmt.Sprintf(", note %d/100", *it.ScoreTotal)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// raceDateLine ajoute le jour de la course au récapitulatif envoyé au coach, quand il
// est connu : sans lui, il ne connaît l'échéance qu'à la semaine près.
func raceDateLine(raceDate string) string {
	if strings.TrimSpace(raceDate) == "" {
		return ""
	}
	return "\n- Date de la course : " + raceDate
}
