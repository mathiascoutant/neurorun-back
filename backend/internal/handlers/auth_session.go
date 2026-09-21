package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"runapp/internal/auth"
	"runapp/internal/models"
	"runapp/internal/store"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Codes applicatifs renvoyés au client sur le circuit de session. Le client s’en
// sert pour trancher entre « session réellement finie » (→ écran de connexion) et
// « le serveur n’a pas répondu » (→ on garde la session et on réessaie).
const (
	// CodeRefreshInvalid : jeton inconnu, expiré, révoqué ou rejoué. Irrattrapable.
	CodeRefreshInvalid = "refresh_invalid"
	// CodeTokenInvalid : jeton d’accès absent, expiré ou porteur d’un compte
	// disparu. Le client tente un rafraîchissement avant d’abandonner. Ce code
	// est ce qui distingue ce 401 d’un « mot de passe actuel incorrect », qui
	// n’a rien à voir avec la session.
	CodeTokenInvalid = "token_invalid"
)

// writeTokenRejected : 401 émis par l’AuthMiddleware, et lui seul.
func writeTokenRejected(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusUnauthorized, map[string]string{
		"error": msg,
		"code":  CodeTokenInvalid,
	})
}

// sessionTokens : ce qu’une connexion réussie remet au client.
type sessionTokens struct {
	// Token : JWT d’accès. Le nom est celui d’origine, et il le reste : les
	// versions de l’app déjà installées ne lisent que ce champ.
	Token        string
	RefreshToken string
	// ExpiresIn : durée de vie du JWT d’accès, en secondes.
	ExpiresIn int
}

// deviceLabel : libellé indicatif de l’appareil, pour retrouver une session dans
// la base. Tronqué court — c’est une aide au diagnostic, pas une donnée.
func deviceLabel(r *http.Request) string {
	d := strings.TrimSpace(r.Header.Get("X-Client-Device"))
	if d == "" {
		d = strings.TrimSpace(r.UserAgent())
	}
	if len(d) > 120 {
		d = d[:120]
	}
	return d
}

// issueSession ouvre une session : un JWT d’accès court et un jeton de
// rafraîchissement rotatif enregistré en base.
//
// L’échec d’écriture du refresh n’annule pas la connexion — l’utilisateur entre
// avec son seul jeton d’accès, comme avant l’existence du refresh. Mieux vaut
// une session de 7 jours qu’un « connexion impossible » sur un incident Mongo.
func (h *Handlers) issueSession(ctx context.Context, u *models.User, device string) (sessionTokens, error) {
	access, err := auth.SignJWT(u.ID.Hex(), h.cfg.JWTSecret, h.cfg.AccessTokenTTL)
	if err != nil {
		return sessionTokens{}, err
	}
	out := sessionTokens{
		Token:     access,
		ExpiresIn: int(h.cfg.AccessTokenTTL.Seconds()),
	}

	refresh, err := auth.NewRefreshToken()
	if err != nil {
		log.Printf("session %s: génération refresh: %v", u.ID.Hex(), err)
		return out, nil
	}
	if _, err := h.db.CreateRefreshToken(
		ctx, u.ID, auth.HashRefreshToken(refresh), primitive.NilObjectID, h.cfg.RefreshTokenTTL, device,
	); err != nil {
		log.Printf("session %s: enregistrement refresh: %v", u.ID.Hex(), err)
		return out, nil
	}
	out.RefreshToken = refresh
	return out, nil
}

// writeSession : réponse commune à l’inscription, la connexion et le refresh.
func (h *Handlers) writeSession(
	w http.ResponseWriter,
	status int,
	t sessionTokens,
	u *models.User,
	caps map[string]bool,
) {
	body := map[string]any{
		"token":      t.Token,
		"expires_in": t.ExpiresIn,
		"user":       userPublic(u, caps),
	}
	// Absent si l’enregistrement a échoué : le client retombe alors sur le
	// fonctionnement d’avant, sans rafraîchissement.
	if t.RefreshToken != "" {
		body["refresh_token"] = t.RefreshToken
	}
	writeJSON(w, status, body)
}

type refreshBody struct {
	RefreshToken string `json:"refresh_token"`
}

// Refresh POST /api/auth/refresh — public : le jeton d’accès est probablement
// expiré, c’est précisément pourquoi on passe ici.
//
// Le jeton présenté est consommé et remplacé (rotation). Un jeton déjà échangé
// fait sauter toute la lignée : on ne peut pas distinguer un rejeu légitime d’un
// vol, et se tromper dans ce sens ne coûte qu’une reconnexion.
func (h *Handlers) Refresh(w http.ResponseWriter, r *http.Request) {
	var b refreshBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "requête invalide"})
		return
	}
	presented := strings.TrimSpace(b.RefreshToken)
	if presented == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "jeton de rafraîchissement manquant",
			"code":  CodeRefreshInvalid,
		})
		return
	}

	old, err := h.db.ConsumeRefreshToken(r.Context(), auth.HashRefreshToken(presented))
	switch {
	case errors.Is(err, store.ErrRefreshReplay):
		log.Printf("refresh: rejeu détecté, lignée révoquée")
		writeSessionExpired(w, "session expirée — reconnecte-toi")
		return
	case errors.Is(err, store.ErrNotFound):
		writeSessionExpired(w, "session expirée — reconnecte-toi")
		return
	case err != nil:
		// Incident serveur : surtout pas 401, sinon le client déconnecte
		// quelqu’un dont la session est parfaitement valide.
		log.Printf("refresh: lecture: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "erreur serveur"})
		return
	}

	u, err := h.db.FindUserByID(r.Context(), old.UserID)
	if errors.Is(err, store.ErrNotFound) {
		_ = h.db.RevokeRefreshFamily(r.Context(), old.FamilyID)
		writeSessionExpired(w, "compte introuvable")
		return
	}
	if err != nil {
		log.Printf("refresh: utilisateur %s: %v", old.UserID.Hex(), err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "erreur serveur"})
		return
	}

	// Avant-première : la session longue s’arrête ici. On révoque la lignée — sinon les autres
	// appareils du compte continueraient de tourner jusqu’à expiration de leur jeton d’accès.
	if msg := h.betaLock(r.Context(), u); msg != "" {
		_ = h.db.RevokeRefreshFamily(r.Context(), old.FamilyID)
		writeSessionExpired(w, msg)
		return
	}

	access, err := auth.SignJWT(u.ID.Hex(), h.cfg.JWTSecret, h.cfg.AccessTokenTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token"})
		return
	}
	next, err := auth.NewRefreshToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token"})
		return
	}
	if _, err := h.db.RotateRefreshToken(
		r.Context(), old, auth.HashRefreshToken(next), h.cfg.RefreshTokenTTL, deviceLabel(r),
	); err != nil {
		// Le jeton présenté vient d'être consommé : sans successeur enregistré,
		// le client n'aurait plus rien de valable. On coupe proprement.
		log.Printf("refresh: rotation: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "erreur serveur"})
		return
	}

	caps, err := h.capabilitiesForUser(r.Context(), u)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config"})
		return
	}
	h.writeSession(w, http.StatusOK, sessionTokens{
		Token:        access,
		RefreshToken: next,
		ExpiresIn:    int(h.cfg.AccessTokenTTL.Seconds()),
	}, u, caps)
}

// writeSessionExpired : le seul 401 que le client doit traduire par « retourne à
// l’écran de connexion ». Le code applicatif le distingue d’un 401 de passage.
func writeSessionExpired(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusUnauthorized, map[string]string{
		"error": msg,
		"code":  CodeRefreshInvalid,
	})
}

// Logout POST /api/auth/logout — public : le jeton d’accès peut déjà être
// expiré, et il n’y a aucune raison d’empêcher quelqu’un de fermer sa session.
// Idempotent : un jeton inconnu ou déjà coupé répond ok.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	var b refreshBody
	// Corps absent ou illisible : il n’y a rien à révoquer, mais la
	// déconnexion côté client reste un succès.
	_ = json.NewDecoder(r.Body).Decode(&b)

	if presented := strings.TrimSpace(b.RefreshToken); presented != "" {
		if err := h.db.RevokeRefreshToken(r.Context(), auth.HashRefreshToken(presented)); err != nil {
			log.Printf("logout: révocation: %v", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
