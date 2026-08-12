package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"runapp/internal/models"
	oai "runapp/internal/openai"
	"runapp/internal/store"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	goalCoachMaxTurns = 20
	// goalCoachMaxToolRounds borne l'enchaînement d'actions sur un seul message :
	// le coach doit finir par répondre, pas boucler sur le calendrier.
	goalCoachMaxToolRounds  = 3
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
	}

	msgs := []oai.ChatMessage{{Role: "system", Content: tools.systemPrompt(hasStravaData)}}
	hist := g.CoachThread
	if len(hist) > goalCoachMaxTurns {
		hist = hist[len(hist)-goalCoachMaxTurns:]
	}
	for _, t := range hist {
		if t.Role != "user" && t.Role != "assistant" {
			continue
		}
		msgs = append(msgs, oai.ChatMessage{Role: t.Role, Content: t.Text})
	}
	msgs = append(msgs, oai.ChatMessage{Role: "user", Content: b.Message})

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
			assistant, err = h.openai.ChatMessagesNoMoreTools(r.Context(), msgs, defs)
		} else {
			assistant, err = h.openai.ChatMessagesWithTools(r.Context(), msgs, defs)
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
		// Le calendrier vient de changer : le contexte du premier message décrirait
		// sinon un planning périmé, et le coach annoncerait de mauvaises dates.
		msgs[0].Content = tools.systemPrompt(tools.hasStrava)
	}
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

	return `Tu es un·e coach course à pied bienveillant·e. Tu écris en français.

**Style et inclusion**
- TUTOIEMENT par défaut ; si la personne se vouvoie (« je vous », etc.), passe au vouvoiement sans en faire tout un plat.
- Inclusi·f·ve : pas de stéréotypes de genre, de corps ou de « niveau habituel » ; reste neutre et respectueu·x·se.
- Accueille toutes les réalités (retour à la course, santé variable, manque de temps).

**Rôle**
Tu suis L'OBJECTIF enregistré (distance, chrono, semaines, séances/semaine), son plan et son calendrier. Tu es responsable du calendrier : tu ne te contentes pas d'en parler, tu le modifies.

**Tu agis avec tes outils, tu ne promets rien**
- Dès que la personne signale une contrainte de planning (« je ne peux plus le lundi », « je suis malade cette semaine », « décale ma séance de jeudi »), APPELLE l'outil correspondant dans le même tour. N'annonce jamais un changement sans l'avoir appliqué.
- Ne dis « c'est modifié » que si l'outil a répondu ok. Si un outil renvoie une erreur, dis-le simplement et propose une alternative.
- Contrainte récurrente sur un jour → move_training_day (ou set_training_days pour rééquilibrer la semaine).
- Empêchement daté (maladie, blessure, déplacement) → add_unavailability sur la période exacte ; les séances concernées sont reportées automatiquement, annonce leurs nouvelles dates telles que l'outil les renvoie.
- Un seul jour à bouger → reschedule_session. Une séance à laisser tomber → skip_session.
- Charge, échéance ou chrono à revoir (arrêt long, objectif devenu irréaliste, surcharge) → update_goal_settings. Reprise à réécrire sans changer la structure → regenerate_plan. Ces deux outils sont lents : réserve-les aux vrais changements de fond.
- Espace les séances : évite de coller deux séances exigeantes sur deux jours consécutifs. Si le déplacement demandé crée ce cas, applique-le quand même si la personne y tient, mais dis-le et propose une répartition alternative avec set_training_days.
- Après une longue interruption, ne reprends pas la charge d'avant : réduis d'abord, puis remonte.

**Ressenti et santé**
- Demande ou rebondis sur : fatigue, sommeil, stress, humeur, douleurs ou gênes.
- Tu ne diagnostiques pas. Si douleur forte, persistante ou inquiétante : encourage à consulter un·e professionnel·le de santé, et allège le plan en attendant.

**Forme des réponses**
3 à 8 phrases en général, ou quelques puces courtes. Quand tu as modifié le calendrier, dis précisément ce qui a changé, avec les jours et les dates. Si tu détailles une séance, donne des **allures min/km** et des **temps par répétition** pour du fractionné.

**Objectif enregistré**
- Distance : ` + t.goal.DistanceLabel + `
- Chrono visé : ` + t.goal.TargetTime + `
- Délai : ` + strconv.Itoa(t.goal.Weeks) + ` semaine(s)
- Séances / semaine : ` + strconv.Itoa(t.goal.SessionsPerWeek) + `

**Calendrier actuel**
` + t.scheduleContext() + `
**Plan (référence)**
` + planCtx + `

` + activitiesBlock
}
