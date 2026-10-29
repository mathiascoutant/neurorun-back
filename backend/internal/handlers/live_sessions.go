package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"runapp/internal/models"
	"runapp/internal/push"
	"runapp/internal/store"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	// Un faux départ suivi d'un vrai, ou une montre relancée en cours de route, ne
	// doit pas faire sonner deux fois le téléphone des amis.
	liveStartRenotifyAfter = 20 * time.Minute
	// Entre deux boosts du même ami sur la même course : assez pour que la voix ait
	// fini de parler, trop court pour qu'on le ressente comme une limite.
	liveBoostCooldown = 15 * time.Second
	maxLiveTargetKm   = 300
)

// liveStartNotificationText : le titre dit ce que l'ami vient de lancer, avec ses
// mots — « son 5 km » quand il a choisi une distance sur le téléphone, le type de
// séance sinon. Le corps invite à aller l'encourager pendant qu'il court.
func liveStartNotificationText(name, sessionType string, targetKm float64) (title, body string) {
	var what string
	switch sessionType {
	case models.RunTypeInterval:
		what = "un fractionné"
		body = "Envoie-lui un boost pour tenir les répétitions 💪"
	case models.RunTypeEndurance:
		what = "une sortie endurance"
		body = "Viens l'encourager en direct 👏"
	default:
		what = "une sortie libre"
		body = "Viens l'encourager en direct 👏"
	}
	if targetKm > 0 {
		dist := formatRoundedKm(targetKm * 1000)
		switch sessionType {
		case models.RunTypeInterval:
			what = "son fractionné de " + dist
		case models.RunTypeEndurance:
			what = "son endurance de " + dist
		default:
			what = "son " + dist
		}
	}
	icon := "🏃"
	if sessionType == models.RunTypeInterval {
		icon = "⚡"
	}
	return icon + " " + name + " vient de commencer " + what, body
}

// liveBoostMessage : ce que le coureur lit et entend. La version parlée n'a pas
// d'emoji, la synthèse vocale les lirait en toutes lettres.
func liveBoostMessage(name, kind string) (text, spoken string) {
	switch kind {
	case models.BoostKindFire:
		spoken = name + " dit que tu es en feu !"
		return spoken + " 🔥", spoken
	case models.BoostKindRespect:
		spoken = name + " t'applaudit !"
		return spoken + " 👏", spoken
	default:
		spoken = name + " t'envoie de la force !"
		return spoken + " 💪", spoken
	}
}

// validLiveSessionType : seuls les trois types de séance connus ; vide = inconnu.
func validLiveSessionType(t string) bool {
	return t == "" || t == models.RunTypeFree || t == models.RunTypeEndurance || t == models.RunTypeInterval
}

func liveSessionPublic(s *models.LiveSession) map[string]any {
	out := map[string]any{
		"id":         s.ID.Hex(),
		"source":     s.Source,
		"started_at": s.StartedAt.Format(time.RFC3339),
	}
	if s.SessionType != "" {
		out["session_type"] = s.SessionType
	}
	if s.TargetKm > 0 {
		out["target_km"] = s.TargetKm
	}
	return out
}

// StartLiveSession POST /api/live-sessions — le coureur vient d'appuyer sur
// Démarrer (téléphone ou montre). Les amis sont prévenus en arrière-plan.
func (h *Handlers) StartLiveSession(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	var body struct {
		Source      string  `json:"source"`
		SessionType string  `json:"session_type"`
		TargetKm    float64 `json:"target_km"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corps invalide"})
		return
	}
	source := strings.TrimSpace(body.Source)
	sessionType := strings.TrimSpace(body.SessionType)
	if !models.ValidLiveSessionSource(source) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "appareil inconnu"})
		return
	}
	if !validLiveSessionType(sessionType) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type de séance inconnu"})
		return
	}
	target := body.TargetKm
	if math.IsNaN(target) || target < 0 || target > maxLiveTargetKm {
		target = 0
	}

	last, err := h.db.LastLiveSessionStart(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	s := models.LiveSession{
		UserID:      u.ID,
		Source:      source,
		SessionType: sessionType,
		TargetKm:    math.Round(target*100) / 100,
	}
	if err := h.db.StartLiveSession(r.Context(), &s); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	notify := last.IsZero() || s.StartedAt.Sub(last) >= liveStartRenotifyAfter
	if notify {
		h.notifyFriendsOfLiveStart(u, &s)
	}
	resp := liveSessionPublic(&s)
	resp["friends_notified"] = notify
	writeJSON(w, http.StatusCreated, resp)
}

// EndLiveSession POST /api/live-sessions/{id}/end — arrêt de la course.
func (h *Handlers) EndLiveSession(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	id, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	if err := h.db.EndLiveSession(r.Context(), u.ID, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListLiveSessionBoosts GET /api/live-sessions/{id}/boosts?after= — relu par
// l'appareil du coureur pendant la course, pour annoncer chaque boost.
func (h *Handlers) ListLiveSessionBoosts(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	id, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	s, err := h.db.GetLiveSession(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && s.UserID != u.ID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "course introuvable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	// Sans curseur, on part du départ : un boost envoyé avant le premier relevé
	// n'est pas perdu.
	after := s.StartedAt.Add(-time.Second)
	if raw := strings.TrimSpace(r.URL.Query().Get("after")); raw != "" {
		if t, perr := time.Parse(time.RFC3339Nano, raw); perr == nil {
			after = t
		}
	}
	rows, err := h.db.ListLiveBoostsAfter(r.Context(), s.ID, after, 50)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	ids := make([]primitive.ObjectID, 0, len(rows))
	for _, b := range rows {
		ids = append(ids, b.FromUserID)
	}
	users := h.usersByID(r.Context(), ids)
	out := make([]map[string]any, 0, len(rows))
	for _, b := range rows {
		name := "Un ami"
		if fu := users[b.FromUserID]; fu != nil {
			name = friendDisplayName(fu)
		}
		text, spoken := liveBoostMessage(name, b.Kind)
		out = append(out, map[string]any{
			"id":         b.ID.Hex(),
			"kind":       b.Kind,
			"from_name":  name,
			"text":       text,
			"spoken":     spoken,
			"created_at": b.CreatedAt.Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"boosts": out, "ended": s.EndedAt != nil})
}

// SendLiveBoost POST /api/live-sessions/{id}/boosts — un ami encourage pendant
// la course. Plusieurs boosts sont permis, espacés d'un court délai.
func (h *Handlers) SendLiveBoost(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	id, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	var body struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corps invalide"})
		return
	}
	kind := strings.TrimSpace(body.Kind)
	if !models.ValidBoostKind(kind) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "réaction inconnue"})
		return
	}
	s, err := h.db.GetLiveSession(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "course introuvable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	if s.UserID == u.ID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "impossible de booster sa propre course"})
		return
	}
	friends, err := h.db.AreFriends(r.Context(), u.ID, s.UserID)
	if err != nil || !friends {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "réservé aux amis"})
		return
	}
	now := time.Now().UTC()
	if s.EndedAt != nil || !now.Before(s.ExpiresAt) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "la course est terminée"})
		return
	}
	last, err := h.db.LastLiveBoostFrom(r.Context(), s.ID, u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	if wait := liveBoostCooldown - now.Sub(last); !last.IsZero() && wait > 0 {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":           "boost déjà envoyé, patiente un instant",
			"retry_after_sec": int(math.Ceil(wait.Seconds())),
		})
		return
	}
	b := models.LiveBoost{SessionID: s.ID, RunnerID: s.UserID, FromUserID: u.ID, Kind: kind, CreatedAt: now}
	if err := h.db.InsertLiveBoost(r.Context(), &b); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	// Une Garmin n'interroge pas le serveur : elle affiche et fait vibrer les
	// notifications du téléphone, c'est donc par là que passe le boost.
	if s.Source == models.LiveSessionSourceGarmin {
		h.notifyRunnerOfLiveBoost(u, s, kind)
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":              b.ID.Hex(),
		"kind":            kind,
		"created_at":      b.CreatedAt.Format(time.RFC3339Nano),
		"retry_after_sec": int(liveBoostCooldown.Seconds()),
	})
}

// ListFriendsLive GET /api/friends/live — amis en train de courir, pour l'écran
// Boost et l'ouverture depuis la notification de départ.
func (h *Handlers) ListFriendsLive(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	friendIDs, err := h.db.ListFriendIDs(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	rows, err := h.db.ListOpenLiveSessionsByUsers(r.Context(), friendRunRecipients(u.ID, friendIDs))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	userIDs := make([]primitive.ObjectID, 0, len(rows))
	sessionIDs := make([]primitive.ObjectID, 0, len(rows))
	for _, s := range rows {
		userIDs = append(userIDs, s.UserID)
		sessionIDs = append(sessionIDs, s.ID)
	}
	users := h.usersByID(r.Context(), userIDs)
	counts, _ := h.db.CountLiveBoostsBySessions(r.Context(), sessionIDs)
	out := make([]map[string]any, 0, len(rows))
	for i := range rows {
		s := &rows[i]
		fu := users[s.UserID]
		if fu == nil {
			continue
		}
		item := liveSessionPublic(s)
		item["friend"] = friendPublic(fu)
		item["boosts_count"] = counts[s.ID]
		if last, lerr := h.db.LastLiveBoostFrom(r.Context(), s.ID, u.ID); lerr == nil && !last.IsZero() {
			item["my_last_boost_at"] = last.Format(time.RFC3339Nano)
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// notifyFriendsOfLiveStart prévient les amis en arrière-plan : le départ de la
// course ne doit ni attendre Expo, ni échouer à cause de lui.
func (h *Handlers) notifyFriendsOfLiveStart(author *models.User, s *models.LiveSession) {
	name := friendDisplayName(author)
	title, body := liveStartNotificationText(name, s.SessionType, s.TargetKm)
	msg := push.Message{
		Title:    title,
		Body:     body,
		Sound:    "default",
		Priority: "high",
		Data: map[string]any{
			"kind":        "friend_live",
			"session_id":  s.ID.Hex(),
			"friend_id":   author.ID.Hex(),
			"friend_name": name,
		},
	}
	authorID := author.ID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()
		friendIDs, err := h.db.ListFriendIDs(ctx, authorID)
		if err != nil {
			log.Printf("push départ d'ami: amis: %v", err)
			return
		}
		recipients := friendRunRecipients(authorID, friendIDs)
		if len(recipients) == 0 {
			return
		}
		tokens, err := h.db.ListPushTokensByUsers(ctx, recipients)
		if err != nil {
			log.Printf("push départ d'ami: jetons: %v", err)
			return
		}
		h.sendToTokens(ctx, tokens, msg)
	}()
}

// notifyRunnerOfLiveBoost : le boost part en notification sur le téléphone du
// coureur, que sa montre Garmin relaie avec une vibration.
func (h *Handlers) notifyRunnerOfLiveBoost(from *models.User, s *models.LiveSession, kind string) {
	text, _ := liveBoostMessage(friendDisplayName(from), kind)
	msg := push.Message{
		Title:    text,
		Body:     "Pendant ta course",
		Sound:    "default",
		Priority: "high",
		Data:     map[string]any{"kind": "live_boost", "session_id": s.ID.Hex()},
	}
	runnerID := s.UserID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()
		tokens, err := h.db.ListPushTokensByUser(ctx, runnerID)
		if err != nil {
			log.Printf("push boost en direct: jetons: %v", err)
			return
		}
		h.sendToTokens(ctx, tokens, msg)
	}()
}
