package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"runapp/internal/models"
	oai "runapp/internal/openai"
	"runapp/internal/strava"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

/*
 * Avancement réel d'une génération de plan.
 *
 * Une barre calée sur le temps écoulé ment dès que l'appel est plus lent ou plus
 * rapide que prévu. Ici chaque unité franchie correspond à un fait vérifiable :
 * une section du plan effectivement rédigée par le modèle, une semaine de
 * calendrier écrite, les séances extraites, l'enregistrement fait. La réponse du
 * modèle étant diffusée au fil de l'eau, ces faits sont observés en direct.
 */

// planProgress : `Done` unités franchies sur `Total`, et ce qui vient de l'être.
type planProgress struct {
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Label string `json:"label"`
}

// Sections de niveau 2 imposées au modèle par le prompt (les deux variantes en ont
// le même nombre). Sert de dénominateur, pas de contrôle : un plan qui en compte
// moins verra simplement l'unité suivante franchie par l'étape d'après.
const planSectionCount = 8

var (
	rePlanSection = regexp.MustCompile(`(?m)^##\s+\S`)
	rePlanWeek    = regexp.MustCompile(`(?mi)^###\s+Semaine\s+(\d+)`)
)

// planWritingUnits : unités que la rédaction peut franchir (sections + semaines).
func planWritingUnits(weeks int) int {
	if weeks < 0 {
		weeks = 0
	}
	return planSectionCount + weeks
}

// planTotalUnits ajoute les deux étapes qui suivent la rédaction : extraction des
// séances, puis enregistrement.
func planTotalUnits(weeks int) int {
	return planWritingUnits(weeks) + 2
}

// streamPlan diffuse la réponse du modèle et signale chaque titre franchi.
func (h *Handlers) streamPlan(
	ctx context.Context,
	system, userQ string,
	weeks int,
	onProgress func(planProgress),
) (string, error) {
	total := planTotalUnits(weeks)
	maxWriting := planWritingUnits(weeks)

	var seen strings.Builder
	sections, weeksWritten, lastDone := 0, 0, 0

	_, err := h.openai.ChatMessagesStream(ctx, []oai.ChatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: userQ},
	}, func(delta string) {
		seen.WriteString(delta)
		sections, weeksWritten = planUnitsWritten(seen.String(), weeks)

		done := sections + weeksWritten
		if done > maxWriting {
			done = maxWriting
		}
		// L'avancement ne recule jamais : il n'y a pas de retour en arrière dans un
		// texte qui s'écrit, et une jauge qui redescend inquiète à tort.
		if done <= lastDone {
			return
		}
		lastDone = done
		onProgress(planProgress{Done: done, Total: total, Label: planStepLabel(weeksWritten, weeks, sections)})
	})
	if err != nil {
		return "", err
	}
	return seen.String(), nil
}

// planUnitsWritten compte, dans le texte reçu jusqu'ici, les sections et les semaines
// dont le titre est complet. Une ligne encore en cours d'arrivée ne compte pas :
// « ## Sé » pourrait être autre chose, et un jalon annoncé trop tôt serait faux.
func planUnitsWritten(text string, weeks int) (sections, weeksWritten int) {
	i := strings.LastIndexByte(text, '\n')
	if i < 0 {
		return 0, 0
	}
	complete := text[:i+1]
	sections = len(rePlanSection.FindAllStringIndex(complete, -1))
	if sections > planSectionCount {
		sections = planSectionCount
	}
	weeksWritten = len(rePlanWeek.FindAllStringIndex(complete, -1))
	if weeksWritten > weeks {
		weeksWritten = weeks
	}
	return sections, weeksWritten
}

// planStepLabel décrit le dernier fait observé, en clair.
func planStepLabel(weeksWritten, weeks, sections int) string {
	if weeksWritten > 0 {
		return fmt.Sprintf("Semaine %d sur %d rédigée", weeksWritten, weeks)
	}
	if sections > 0 {
		return fmt.Sprintf("%d section%s rédigée%s", sections, plural(sections), plural(sections))
	}
	return "Rédaction du plan"
}

func plural(n int) string {
	if n > 1 {
		return "s"
	}
	return ""
}

// sseWriter sérialise l'écriture d'événements SSE et pousse chaque message tout de
// suite : un avancement mis en tampon n'avance plus rien.
type sseWriter struct {
	w  http.ResponseWriter
	f  http.Flusher
	mu sync.Mutex
}

func newSSEWriter(w http.ResponseWriter) (*sseWriter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Sans cet en-tête, un nginx en frontal garde les événements en tampon et la
	// progression arrive d'un bloc à la fin — c'est-à-dire jamais.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &sseWriter{w: w, f: f}, true
}

func (s *sseWriter) send(payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.w, "data: %s\n\n", raw)
	s.f.Flush()
}

// ReplanGoalWithStravaStream fait le même travail que ReplanGoalWithStrava, en
// rendant compte de l'avancement réel au fil de la génération.
//
// Les erreurs passent par le flux, pas par le code HTTP : l'en-tête 200 part dès la
// connexion établie, bien avant qu'on sache si la génération aboutira.
func (h *Handlers) ReplanGoalWithStravaStream(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "goals") {
		return
	}

	sse, ok := newSSEWriter(w)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "flux non supporté"})
		return
	}
	fail := func(msg, code string) {
		sse.send(map[string]any{"step": "error", "error": msg, "code": code})
	}

	if !u.HasStrava() {
		fail("strava non lié", stravaUnlinkedCode)
		return
	}
	if strings.TrimSpace(h.cfg.OpenAIAPIKey) == "" {
		fail("génération indisponible", "")
		return
	}

	gid, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		fail("id invalide", "")
		return
	}
	g, err := h.db.GetGoalByUser(r.Context(), u.ID, gid)
	if err != nil {
		fail("objectif introuvable", "")
		return
	}

	total := planTotalUnits(g.Weeks)
	sse.send(planProgress{Done: 0, Total: total, Label: "Lecture de tes sorties Strava"})

	access, err := h.ensureStravaAccess(r.Context(), u)
	if err != nil {
		h.forgetStravaIfRevoked(r.Context(), u, err)
		if errors.Is(err, strava.ErrUnauthorized) {
			fail("l’accès à Strava a été révoqué : réassocie ton compte", stravaUnlinkedCode)
			return
		}
		fail("impossible d’accéder à Strava", "")
		return
	}
	acts, err := h.strava.ActivitiesSummary(r.Context(), access, 50)
	if err != nil {
		h.forgetStravaIfRevoked(r.Context(), u, err)
		if errors.Is(err, strava.ErrUnauthorized) {
			fail("l’accès à Strava a été révoqué : réassocie ton compte", stravaUnlinkedCode)
			return
		}
		fail("erreur Strava", "")
		return
	}
	actsJSON, _ := json.Marshal(acts)

	plan, planned, err := h.synthesizeTrainingPlan(
		r.Context(), actsJSON, g.DistanceLabel, g.TargetTime, g.Weeks, g.SessionsPerWeek, true,
		func(p planProgress) { sse.send(p) },
	)
	if err != nil {
		fail("le coach n’a pas pu écrire le plan", "")
		return
	}

	if err := h.db.UpdateGoalTrainingFields(
		r.Context(), u.ID, gid, plan, planned,
		g.Weeks, g.SessionsPerWeek, g.TargetTime, g.CalendarDayOffsets, false,
	); err != nil {
		fail("sauvegarde du plan impossible", "")
		return
	}
	sse.send(planProgress{Done: total, Total: total, Label: "Plan enregistré"})

	refreshed, err := h.db.GetGoalByUser(r.Context(), u.ID, gid)
	if err != nil {
		fail("plan enregistré, rechargement impossible", "")
		return
	}
	sse.send(map[string]any{"step": "done", "goal": refreshed})
}

// CreateGoalStream crée un objectif comme CreateGoal, en rendant compte de
// l'avancement réel de la rédaction du plan — c'est l'attente la plus longue de
// l'appli, et la seule où l'utilisateur n'a encore rien à regarder.
func (h *Handlers) CreateGoalStream(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "goals") {
		return
	}

	// Le corps est lu avant d'ouvrir le flux : une requête mal formée mérite encore
	// un vrai code HTTP, que le client sait déjà traiter.
	b, label, distKm, targetTime, errHTTP, errMsg := validateGoalPayload(r)
	if errHTTP != 0 {
		writeJSON(w, errHTTP, map[string]string{"error": errMsg})
		return
	}

	sse, ok := newSSEWriter(w)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "flux non supporté"})
		return
	}
	fail := func(msg, code string) {
		sse.send(map[string]any{"step": "error", "error": msg, "code": code})
	}

	total := planTotalUnits(b.Weeks)
	hasStravaData := u.HasStrava()

	actsJSON := []byte("[]")
	if hasStravaData {
		sse.send(planProgress{Done: 0, Total: total, Label: "Lecture de tes sorties Strava"})
		access, err := h.ensureStravaAccess(r.Context(), u)
		if err == nil {
			var acts any
			acts, err = h.strava.ActivitiesSummary(r.Context(), access, 50)
			if err == nil {
				actsJSON, _ = json.Marshal(acts)
			}
		}
		if err != nil {
			h.forgetStravaIfRevoked(r.Context(), u, err)
			if errors.Is(err, strava.ErrUnauthorized) {
				fail("l’accès à Strava a été révoqué : réassocie ton compte", stravaUnlinkedCode)
				return
			}
			fail("impossible de lire tes sorties Strava", "")
			return
		}
	} else {
		sse.send(planProgress{Done: 0, Total: total, Label: "Préparation du plan"})
	}

	plan, planned, err := h.synthesizeTrainingPlan(
		r.Context(), actsJSON, label, targetTime, b.Weeks, b.SessionsPerWeek, hasStravaData,
		func(p planProgress) { sse.send(p) },
	)
	if err != nil {
		fail("le coach n’a pas pu écrire le plan", "")
		return
	}

	g := &models.Goal{
		UserID:                u.ID,
		DistanceKm:            distKm,
		DistanceLabel:         label,
		Weeks:                 b.Weeks,
		SessionsPerWeek:       b.SessionsPerWeek,
		TargetTime:            targetTime,
		Plan:                  plan,
		PlannedSessions:       planned,
		CoachThread:           []models.ChatTurn{goalWelcomeTurn()},
		PlanWithoutStravaData: !hasStravaData,
	}
	if err := h.db.CreateGoal(r.Context(), g); err != nil {
		fail("sauvegarde de l’objectif impossible", "")
		return
	}
	sse.send(planProgress{Done: total, Total: total, Label: "Objectif enregistré"})
	sse.send(map[string]any{"step": "done", "goal": g})
}
