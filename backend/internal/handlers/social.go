package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"runapp/internal/models"
	"runapp/internal/push"
	"runapp/internal/store"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// friendDisplayName : prénom seul si possible — c'est ce qui s'affiche dans le fil
// et dans les notifications (« Lucas t'a boosté ! »).
func friendDisplayName(u *models.User) string {
	first := strings.TrimSpace(u.FirstName)
	if first != "" {
		return first
	}
	last := strings.TrimSpace(u.LastName)
	if last != "" {
		return last
	}
	return "Un ami"
}

func friendPublic(u *models.User) map[string]any {
	return map[string]any{
		"id":           u.ID.Hex(),
		"first_name":   u.FirstName,
		"last_name":    u.LastName,
		"display_name": friendDisplayName(u),
	}
}

// usersByID charge en une requête les comptes référencés par le fil / les demandes.
func (h *Handlers) usersByID(ctx context.Context, ids []primitive.ObjectID) map[primitive.ObjectID]*models.User {
	rows, err := h.db.FindUsersByIDs(ctx, ids)
	if err != nil {
		return map[primitive.ObjectID]*models.User{}
	}
	out := make(map[primitive.ObjectID]*models.User, len(rows))
	for id := range rows {
		u := rows[id]
		out[id] = &u
	}
	return out
}

// SearchUsers GET /api/users/search?q= — annuaire pour ajouter un ami. Chaque résultat
// porte l'état de la relation, pour que l'app affiche « Ajouter », « Demande envoyée »
// ou « Déjà ami » sans second appel.
func (h *Handlers) SearchUsers(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 {
		writeJSON(w, http.StatusOK, map[string]any{"users": []any{}})
		return
	}

	found, err := h.db.SearchUsersByName(r.Context(), q, u.ID, 20)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "recherche impossible"})
		return
	}

	ids := make([]primitive.ObjectID, 0, len(found))
	for i := range found {
		ids = append(ids, found[i].ID)
	}
	relations, err := h.db.FriendshipsWith(r.Context(), u.ID, ids)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "recherche impossible"})
		return
	}

	out := make([]map[string]any, 0, len(found))
	for i := range found {
		other := &found[i]
		row := friendPublic(other)
		row["relation"] = "none"
		if f, ok := relations[other.ID]; ok {
			switch {
			case f.Status == models.FriendshipAccepted:
				row["relation"] = "friend"
			case f.RequesterID == u.ID:
				row["relation"] = "request_sent"
			default:
				row["relation"] = "request_received"
				row["request_id"] = f.ID.Hex()
			}
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

// ListFriends GET /api/friends
func (h *Handlers) ListFriends(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	ids, err := h.db.ListFriendIDs(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	users := h.usersByID(r.Context(), ids)
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		if fu, ok := users[id]; ok {
			out = append(out, friendPublic(fu))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"friends": out})
}

// SendFriendRequest POST /api/friends/requests {user_id}
func (h *Handlers) SendFriendRequest(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	var body struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corps invalide"})
		return
	}
	target, err := primitive.ObjectIDFromHex(strings.TrimSpace(body.UserID))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "utilisateur invalide"})
		return
	}
	if target == u.ID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "impossible de s’ajouter soi-même"})
		return
	}
	if _, err := h.db.FindUserByID(r.Context(), target); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "utilisateur introuvable"})
		return
	}

	f, err := h.db.SendFriendRequest(r.Context(), u.ID, target)
	if errors.Is(err, store.ErrFriendshipExists) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "une demande existe déjà"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "envoi impossible"})
		return
	}

	h.notifyFriendRequest(u, target)
	writeJSON(w, http.StatusCreated, map[string]any{"id": f.ID.Hex(), "status": f.Status})
}

// ListFriendRequests GET /api/friends/requests — reçues et envoyées, en attente.
func (h *Handlers) ListFriendRequests(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	incoming, outgoing, err := h.db.ListPendingRequests(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}

	ids := make([]primitive.ObjectID, 0, len(incoming)+len(outgoing))
	for i := range incoming {
		ids = append(ids, incoming[i].RequesterID)
	}
	for i := range outgoing {
		ids = append(ids, outgoing[i].AddresseeID)
	}
	users := h.usersByID(r.Context(), ids)

	pack := func(rows []models.Friendship, otherOf func(models.Friendship) primitive.ObjectID) []map[string]any {
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			other, ok := users[otherOf(row)]
			if !ok {
				continue
			}
			out = append(out, map[string]any{
				"id":         row.ID.Hex(),
				"created_at": row.CreatedAt.UTC().Format(time.RFC3339),
				"user":       friendPublic(other),
			})
		}
		return out
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"incoming": pack(incoming, func(f models.Friendship) primitive.ObjectID { return f.RequesterID }),
		"outgoing": pack(outgoing, func(f models.Friendship) primitive.ObjectID { return f.AddresseeID }),
	})
}

// RespondFriendRequest POST /api/friends/requests/{id}/respond {accept}
func (h *Handlers) RespondFriendRequest(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	reqID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	var body struct {
		Accept bool `json:"accept"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corps invalide"})
		return
	}

	if !body.Accept {
		if err := h.db.DeclineFriendRequest(r.Context(), reqID, u.ID); err != nil {
			h.writeFriendRequestErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "declined"})
		return
	}

	f, err := h.db.AcceptFriendRequest(r.Context(), reqID, u.ID)
	if err != nil {
		h.writeFriendRequestErr(w, err)
		return
	}
	h.notifyFriendAccepted(u, f.RequesterID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": models.FriendshipAccepted})
}

// CancelFriendRequest DELETE /api/friends/requests/{id} — l'émetteur retire sa demande.
func (h *Handlers) CancelFriendRequest(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	reqID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	if err := h.db.CancelFriendRequest(r.Context(), reqID, u.ID); err != nil {
		h.writeFriendRequestErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RemoveFriend DELETE /api/friends/{id} — {id} est l'identifiant de l'ami, pas de la relation.
func (h *Handlers) RemoveFriend(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	otherID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	if err := h.db.RemoveFriend(r.Context(), u.ID, otherID); err != nil {
		h.writeFriendRequestErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handlers) writeFriendRequestErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "demande introuvable"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "opération impossible"})
}

// BoostFeed GET /api/boost/feed?limit&before — courses des amis, de la plus récente à la
// plus ancienne, avec le décompte des réactions et celle de l'appelant.
func (h *Handlers) BoostFeed(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)

	friendIDs, err := h.db.ListFriendIDs(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	if len(friendIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}

	limit := 20
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	var before time.Time
	if s := strings.TrimSpace(r.URL.Query().Get("before")); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			before = t
		}
	}

	runs, err := h.db.ListLiveRunsByUsersBefore(r.Context(), friendIDs, before, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}

	runIDs := make([]primitive.ObjectID, 0, len(runs))
	ownerIDs := make([]primitive.ObjectID, 0, len(runs))
	for i := range runs {
		runIDs = append(runIDs, runs[i].ID)
		ownerIDs = append(ownerIDs, runs[i].UserID)
	}
	boostsByRun, err := h.db.ListBoostsForRuns(r.Context(), runIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	owners := h.usersByID(r.Context(), ownerIDs)

	items := make([]map[string]any, 0, len(runs))
	for i := range runs {
		lr := &runs[i]
		owner, ok := owners[lr.UserID]
		if !ok {
			continue
		}
		counts := map[string]int{
			models.BoostKindBoost:   0,
			models.BoostKindFire:    0,
			models.BoostKindRespect: 0,
		}
		var mine any
		for _, b := range boostsByRun[lr.ID] {
			counts[b.Kind]++
			if b.FromUserID == u.ID {
				mine = b.Kind
			}
		}
		items = append(items, map[string]any{
			"id":                  lr.ID.Hex(),
			"user":                friendPublic(owner),
			"created_at":          lr.CreatedAt.UTC().Format(time.RFC3339),
			"distance_m":          lr.DistanceM,
			"moving_sec":          lr.MovingSec,
			"wall_sec":            lr.WallSec,
			"avg_pace_sec_per_km": lr.AvgPaceSecPerKm,
			"split_count":         len(lr.Splits),
			"elevation_gain_m":    elevationGain(lr),
			"boosts":              counts,
			"my_boost":            mine,
		})
	}

	res := map[string]any{"items": items}
	if len(runs) == limit {
		res["next_before"] = runs[len(runs)-1].CreatedAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, res)
}

func elevationGain(lr *models.LiveRun) float64 {
	if lr.ClientStats == nil {
		return 0
	}
	return lr.ClientStats.ElevationGainM
}

// BoostsReceived GET /api/boost/received — réactions reçues sur ses propres courses.
// Sans ça, une notification « Lucas t'a boosté » ne mène nulle part dans l'app.
func (h *Handlers) BoostsReceived(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)

	limit := int64(20)
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = int64(v)
	}
	rows, err := h.db.ListBoostsReceived(r.Context(), u.ID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	if len(rows) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}

	senderIDs := make([]primitive.ObjectID, 0, len(rows))
	runIDs := make([]primitive.ObjectID, 0, len(rows))
	for i := range rows {
		senderIDs = append(senderIDs, rows[i].FromUserID)
		runIDs = append(runIDs, rows[i].RunID)
	}
	senders := h.usersByID(r.Context(), senderIDs)

	// Les courses sont chargées une par une : la liste est courte et bornée par `limit`.
	runs := make(map[primitive.ObjectID]*models.LiveRun, len(runIDs))
	for _, id := range runIDs {
		if _, seen := runs[id]; seen {
			continue
		}
		if run, err := h.db.GetLiveRunMetaByID(r.Context(), id); err == nil {
			runs[id] = run
		}
	}

	items := make([]map[string]any, 0, len(rows))
	for i := range rows {
		b := &rows[i]
		sender, ok := senders[b.FromUserID]
		if !ok {
			continue
		}
		item := map[string]any{
			"kind":       b.Kind,
			"created_at": b.UpdatedAt.UTC().Format(time.RFC3339),
			"run_id":     b.RunID.Hex(),
			"user":       friendPublic(sender),
		}
		if run, ok := runs[b.RunID]; ok {
			item["distance_m"] = run.DistanceM
			item["avg_pace_sec_per_km"] = run.AvgPaceSecPerKm
			item["run_created_at"] = run.CreatedAt.UTC().Format(time.RFC3339)
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// boostersForRun : qui a réagi à une course, et comment. Renvoie une liste vide
// plutôt que nil pour que le client reçoive toujours un tableau JSON.
func (h *Handlers) boostersForRun(ctx context.Context, runID primitive.ObjectID) []map[string]any {
	out := make([]map[string]any, 0)
	byRun, err := h.db.ListBoostsForRuns(ctx, []primitive.ObjectID{runID})
	if err != nil {
		return out
	}
	rows := byRun[runID]
	if len(rows) == 0 {
		return out
	}
	ids := make([]primitive.ObjectID, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].FromUserID)
	}
	senders := h.usersByID(ctx, ids)
	for i := range rows {
		sender, ok := senders[rows[i].FromUserID]
		if !ok {
			continue
		}
		out = append(out, map[string]any{
			"kind":       rows[i].Kind,
			"created_at": rows[i].UpdatedAt.UTC().Format(time.RFC3339),
			"user":       friendPublic(sender),
		})
	}
	return out
}

// weekStart : lundi 00:00 à Paris. Les kilomètres d'une semaine sont une notion
// civile — en UTC, une sortie du dimanche soir bascule sur la semaine suivante.
func weekStart(now time.Time) time.Time {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		loc = time.UTC
	}
	t := now.In(loc)
	// time.Weekday : dimanche = 0. On ramène à un lundi = 0.
	offset := (int(t.Weekday()) + 6) % 7
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -offset)
}

// BoostLeaderboard GET /api/boost/leaderboard — kilomètres de la semaine, soi inclus.
// Le classement porte sur la distance cumulée et non sur l'allure : tout le monde
// peut ajouter des kilomètres, alors qu'une hiérarchie d'allure est figée d'avance.
func (h *Handlers) BoostLeaderboard(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)

	friendIDs, err := h.db.ListFriendIDs(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	ids := append([]primitive.ObjectID{u.ID}, friendIDs...)

	since := weekStart(time.Now())
	totals, err := h.db.SumDistanceByUsersSince(r.Context(), ids, since)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	users := h.usersByID(r.Context(), ids)

	rows := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		person, ok := users[id]
		if !ok {
			continue
		}
		t := totals[id] // absent = semaine sans course, donc zéro
		rows = append(rows, map[string]any{
			"user":       friendPublic(person),
			"distance_m": t.DistanceM,
			"runs":       t.Runs,
			"is_me":      id == u.ID,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i]["distance_m"].(float64) > rows[j]["distance_m"].(float64)
	})
	for i := range rows {
		rows[i]["rank"] = i + 1
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"week_start": since.UTC().Format(time.RFC3339),
		"rows":       rows,
	})
}

// BoostRun POST /api/live-runs/{id}/boost {kind} — réagir à la course d'un ami.
func (h *Handlers) BoostRun(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	runID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
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

	run, err := h.db.GetLiveRunMetaByID(r.Context(), runID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "course introuvable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	if run.UserID == u.ID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "impossible de booster sa propre course"})
		return
	}
	friends, err := h.db.AreFriends(r.Context(), u.ID, run.UserID)
	if err != nil || !friends {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "réservé aux amis"})
		return
	}

	changed, err := h.db.UpsertBoost(r.Context(), runID, run.UserID, u.ID, kind)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	// Réappuyer sur la même réaction ne renotifie pas : sinon un aller-retour sur les
	// boutons ferait sonner le téléphone du destinataire à chaque appui.
	if changed {
		h.notifyBoost(u, run, kind)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "kind": kind})
}

// RemoveBoostFromRun DELETE /api/live-runs/{id}/boost
func (h *Handlers) RemoveBoostFromRun(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	runID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	if err := h.db.RemoveBoost(r.Context(), runID, u.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RegisterPushToken POST /api/push-token — appareil du compte courant, pour les
// notifications sociales. Sans rapport avec les jetons admin.
func (h *Handlers) RegisterPushToken(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	var body struct {
		Token    string `json:"token"`
		Platform string `json:"platform"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corps invalide"})
		return
	}
	token := strings.TrimSpace(body.Token)
	if !push.IsExpoToken(token) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "jeton Expo invalide"})
		return
	}
	if err := h.db.UpsertPushToken(r.Context(), token, u.ID, body.Platform); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// DeletePushToken DELETE /api/push-token
func (h *Handlers) DeletePushToken(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	var body struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if token := strings.TrimSpace(body.Token); token != "" {
		_ = h.db.DeletePushToken(r.Context(), token)
	} else {
		_ = h.db.DeletePushTokensByUser(r.Context(), u.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- Notifications ---

// sendToUser pousse une notification vers tous les appareils d'un compte et purge
// les jetons que Expo signale comme morts.
func (h *Handlers) sendToUser(ctx context.Context, userID primitive.ObjectID, m push.Message) {
	tokens, err := h.db.ListPushTokensByUser(ctx, userID)
	if err != nil || len(tokens) == 0 {
		return
	}
	invalid, err := h.pushClient.Send(ctx, tokens, m)
	if err != nil && !errors.Is(err, push.ErrNotConfigured) {
		log.Printf("push social: envoi: %v", err)
	}
	if len(invalid) > 0 {
		_ = h.db.DeletePushTokens(ctx, invalid)
	}
}

func (h *Handlers) notifyBoost(from *models.User, run *models.LiveRun, kind string) {
	sender := friendDisplayName(from)
	ownerID := run.UserID
	runID := run.ID.Hex()
	title, body := boostNotificationText(kind, sender, run)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()
		h.sendToUser(ctx, ownerID, push.Message{
			Title: title,
			Body:  body,
			Data:  map[string]any{"kind": "boost", "boost_kind": kind, "run_id": runID},
		})
	}()
}

func (h *Handlers) notifyFriendRequest(from *models.User, toID primitive.ObjectID) {
	sender := friendDisplayName(from)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()
		h.sendToUser(ctx, toID, push.Message{
			Title: "Nouvelle demande d’ami",
			Body:  sender + " veut suivre tes courses",
			Data:  map[string]any{"kind": "friend_request"},
		})
	}()
}

func (h *Handlers) notifyFriendAccepted(accepter *models.User, toID primitive.ObjectID) {
	name := friendDisplayName(accepter)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()
		h.sendToUser(ctx, toID, push.Message{
			Title: "Demande acceptée",
			Body:  name + " a accepté ta demande — ses courses arrivent dans ton fil",
			Data:  map[string]any{"kind": "friend_accepted"},
		})
	}()
}

// boostNotificationText : un texte par réaction. Le corps reprend les chiffres de la
// course pour que la notification se lise sans ouvrir l'app.
func boostNotificationText(kind, sender string, run *models.LiveRun) (title, body string) {
	switch kind {
	case models.BoostKindFire:
		return "🚀 " + sender + " te trouve en feu !",
			fmt.Sprintf("%s à %s 🔥", formatKm(run.DistanceM), formatPace(run.AvgPaceSecPerKm))
	case models.BoostKindRespect:
		return "🫡 " + sender + " t’envoie du Respect",
			formatKm(run.DistanceM) + ". Belle sortie !"
	default:
		return "🔥 " + sender + " t’a boosté !", "Continue comme ça 💪"
	}
}

// formatKm : « 8,2 km » — virgule décimale, comme partout dans l'app.
func formatKm(distanceM float64) string {
	km := distanceM / 1000
	return strings.Replace(strconv.FormatFloat(km, 'f', 1, 64), ".", ",", 1) + " km"
}

// formatPace : « 4:38/km ». Vide si l'allure est absurde (course sans distance).
func formatPace(secPerKm float64) string {
	if secPerKm <= 0 || math.IsNaN(secPerKm) || math.IsInf(secPerKm, 0) {
		return "—"
	}
	total := int(math.Round(secPerKm))
	return fmt.Sprintf("%d:%02d/km", total/60, total%60)
}
