package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"runapp/internal/models"
	"runapp/internal/push"
)

// notifyTimeout : borne l’envoi push, qui tourne hors du cycle de vie de la requête HTTP.
const notifyTimeout = 20 * time.Second

// adminDisplayName : nom affiché dans les notifications admin.
func adminDisplayName(u *models.User) string {
	return models.DisplayName(u.FirstName, u.LastName, u.Email)
}

// planLabel : libellé d’offre en majuscules tel qu’affiché dans la notification (« ALLURE »).
func (h *Handlers) planLabel(ctx context.Context, plan string) string {
	return strings.ToUpper(h.tierDisplayName(ctx, plan))
}

// notifyAdminsSignup — nouveau compte, quelle que soit l’offre choisie à l’inscription.
//
// `device` sert seulement à dire d’où vient l’inscription. Une inscription faite depuis l’app
// est aussi une installation : le dire ici évite d’envoyer une seconde notification
// « installation iOS » dans la même seconde, pour le même évènement.
func (h *Handlers) notifyAdminsSignup(u *models.User, device string) {
	plan := u.EffectivePlan()
	h.notifyAdmins(models.AdminEventSignup, u, plan, device)
}

// notifyAdminsPlanActivated — un compte existant bascule sur une offre payante (parcours web :
// /auth/register crée le compte en standard, puis Stripe active l’offre).
func (h *Handlers) notifyAdminsPlanActivated(u *models.User, plan string) {
	h.notifyAdmins(models.AdminEventPlanActivated, u, plan, "")
}

// notifyAdmins persiste l’évènement puis pousse la notification, en tâche de fond : une panne
// d’Expo ou de Mongo ne doit jamais faire échouer une inscription ou un paiement.
func (h *Handlers) notifyAdmins(kind string, u *models.User, plan, device string) {
	if u == nil {
		return
	}
	snapshot := *u
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()

		label := h.planLabel(ctx, plan)
		name := adminDisplayName(&snapshot)
		title, body := adminNotificationText(kind, name, plan, label, device)

		n := &models.AdminNotification{
			Kind:      kind,
			Title:     title,
			Body:      body,
			UserID:    snapshot.ID,
			UserEmail: snapshot.Email,
			UserName:  name,
			Plan:      plan,
			PlanLabel: label,
			CreatedAt: time.Now().UTC(),
		}
		if err := h.db.InsertAdminNotification(ctx, n); err != nil {
			log.Printf("admin notif: enregistrement %s: %v", kind, err)
		}

		tokens, err := h.db.ListActiveAdminPushTokens(ctx)
		if err != nil {
			log.Printf("admin notif: lecture jetons push: %v", err)
			return
		}
		if len(tokens) == 0 {
			return
		}
		badge, _ := h.db.CountUnreadAdminNotifications(ctx)
		badgeInt := int(badge)
		invalid, err := h.pushClient.Send(ctx, tokens, push.Message{
			Title: title,
			Body:  body,
			Badge: &badgeInt,
			Data: map[string]any{
				"kind":    kind,
				"user_id": snapshot.ID.Hex(),
				"plan":    plan,
			},
		})
		if err != nil {
			log.Printf("admin notif: envoi push: %v", err)
		}
		if len(invalid) > 0 {
			_ = h.db.DeleteAdminPushTokens(ctx, invalid)
		}
	}()
}

func adminNotificationText(kind, name, plan, label, device string) (title, body string) {
	paid := isPaidPlan(plan)
	switch {
	case kind == models.AdminEventIOSInstall:
		return "Nouvelle installation iOS", name + " vient d’ouvrir l’app sur iPhone pour la première fois"
	case kind == models.AdminEventPlanActivated:
		return "Nouvelle offre payante", name + " vient de prendre l’offre " + label
	case paid:
		return "Nouvelle inscription payante" + platformSuffix(device),
			name + " vient de s’inscrire et de prendre l’offre " + label
	default:
		return "Nouvelle inscription" + platformSuffix(device),
			name + " vient de s’inscrire — offre " + label + " (gratuite)"
	}
}

// platformSuffix : d’où vient l’inscription, quand c’est une app. Rien pour le web, qui reste
// le cas ordinaire et n’a pas besoin d’être nommé.
func platformSuffix(device string) string {
	switch models.PlatformFromDevice(device) {
	case models.PlatformIOS:
		return " (app iOS)"
	case models.PlatformAndroid:
		return " (app Android)"
	default:
		return ""
	}
}

type adminNotificationsResponse struct {
	Notifications []models.AdminNotification `json:"notifications"`
	Unread        int64                      `json:"unread"`
}

// AdminListNotifications GET /api/admin/notifications — historique + compteur non lus.
// Monté sous AdminMiddleware : un compte non admin reçoit 403 et ne voit donc jamais rien.
func (h *Handlers) AdminListNotifications(w http.ResponseWriter, r *http.Request) {
	limit := int64(50)
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			limit = n
		}
	}
	items, err := h.db.ListAdminNotifications(r.Context(), limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "notifications"})
		return
	}
	unread, _ := h.db.CountUnreadAdminNotifications(r.Context())
	writeJSON(w, http.StatusOK, adminNotificationsResponse{Notifications: items, Unread: unread})
}

// AdminMarkNotificationsRead POST /api/admin/notifications/read — remet le compteur à zéro.
func (h *Handlers) AdminMarkNotificationsRead(w http.ResponseWriter, r *http.Request) {
	if err := h.db.MarkAdminNotificationsRead(r.Context(), time.Now().UTC()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mise à jour impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "unread": 0})
}

// AdminSendTestNotification POST /api/admin/notifications/test — envoie une notification factice
// aux appareils admin pour vérifier la chaîne Expo → APNs. N’écrit rien dans l’historique.
func (h *Handlers) AdminSendTestNotification(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	tokens, err := h.db.ListActiveAdminPushTokens(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "jetons push"})
		return
	}
	if len(tokens) == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "aucun appareil admin enregistré — ouvre l’app et accepte les notifications",
		})
		return
	}
	label := h.planLabel(r.Context(), models.PlanPerformance)
	invalid, err := h.pushClient.Send(r.Context(), tokens, push.Message{
		Title: "Test NeuroRun",
		Body:  adminDisplayName(u) + " vient de prendre l’offre " + label,
		Data:  map[string]any{"kind": "test"},
	})
	if len(invalid) > 0 {
		_ = h.db.DeleteAdminPushTokens(r.Context(), invalid)
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "envoi impossible : " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "devices": len(tokens)})
}

type pushTokenBody struct {
	Token    string `json:"token"`
	Platform string `json:"platform"`
}

// AdminRegisterPushToken POST /api/admin/push-token — l’app enregistre son jeton Expo
// uniquement quand le compte connecté est admin.
func (h *Handlers) AdminRegisterPushToken(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	var b pushTokenBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	b.Token = strings.TrimSpace(b.Token)
	if !push.IsExpoToken(b.Token) || utf8.RuneCountInString(b.Token) > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "jeton push invalide"})
		return
	}
	b.Platform = strings.TrimSpace(strings.ToLower(b.Platform))
	if b.Platform != "ios" && b.Platform != "android" {
		b.Platform = ""
	}
	if err := h.db.UpsertAdminPushToken(r.Context(), b.Token, u.ID, b.Platform); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// AdminDeletePushToken DELETE /api/admin/push-token — déconnexion ou refus des notifications.
func (h *Handlers) AdminDeletePushToken(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	var b pushTokenBody
	// Corps facultatif : sans jeton précis on purge tous les appareils du compte.
	_ = json.NewDecoder(r.Body).Decode(&b)
	b.Token = strings.TrimSpace(b.Token)
	var err error
	if b.Token != "" {
		err = h.db.DeleteAdminPushToken(r.Context(), b.Token)
	} else {
		err = h.db.DeleteAdminPushTokensByUser(r.Context(), u.ID)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
