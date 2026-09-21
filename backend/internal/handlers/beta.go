package handlers

import (
	"context"
	"log"
	"net/http"
	"time"

	"runapp/internal/models"
)

// Verrou « avant-première ».
//
// Tout se joue côté serveur : l’app installée n’a aucune notion de beta. Elle affiche tel quel
// le champ `error` d’une réponse d’échec, et elle sait déjà se déconnecter proprement sur un
// 401 porteur des codes de session. C’est exactement ce qu’on réutilise ici — d’où le choix des
// statuts, qui n’est pas cosmétique :
//
//   - connexion / inscription → 403 + message : le bandeau rouge de l’écran de connexion
//     affiche la phrase configurée en admin ;
//   - requête authentifiée → 401 `token_invalid`, puis refresh → 401 `refresh_invalid` :
//     l’app vide sa session et retombe sur l’écran de connexion, où elle lira le message.
//
// Un 403 sur une requête authentifiée serait le mauvais choix : l’app ne le lit pas comme une
// session finie, elle repartirait sur son profil en cache et resterait ouverte sur des écrans
// tous en erreur.

const betaCacheTTL = 12 * time.Second

func (h *Handlers) cachedBetaConfig(ctx context.Context) (*models.BetaConfig, error) {
	h.betaMu.RLock()
	if h.betaCache != nil && time.Now().Before(h.betaExpiry) {
		c := *h.betaCache
		h.betaMu.RUnlock()
		return &c, nil
	}
	h.betaMu.RUnlock()

	h.betaMu.Lock()
	defer h.betaMu.Unlock()
	if h.betaCache != nil && time.Now().Before(h.betaExpiry) {
		c := *h.betaCache
		return &c, nil
	}
	cfg, err := h.db.GetBetaConfig(ctx)
	if err != nil {
		return nil, err
	}
	cfg.MergeDefaults()
	h.betaCache = &cfg
	h.betaExpiry = time.Now().Add(betaCacheTTL)
	c := cfg
	return &c, nil
}

func (h *Handlers) invalidateBetaCache() {
	h.betaMu.Lock()
	h.betaCache = nil
	h.betaExpiry = time.Time{}
	h.betaMu.Unlock()
}

// betaLock : message à opposer au compte, ou "" s’il peut entrer.
//
// `u` nil = inscription : personne n’est encore invité, le verrou vaut pour tout le monde.
//
// En cas d’incident de lecture, on ouvre. Un Mongo qui hoquette ne doit pas mettre toute la
// base d’utilisateurs dehors ; l’inverse — laisser passer quelques minutes de trop — se répare
// tout seul au retour de la base.
func (h *Handlers) betaLock(ctx context.Context, u *models.User) string {
	cfg, err := h.cachedBetaConfig(ctx)
	if err != nil {
		log.Printf("avant-première: lecture config: %v", err)
		return ""
	}
	if !cfg.Enabled {
		return ""
	}
	if u != nil && u.CanAccessDuringBeta() {
		return ""
	}
	return cfg.Message
}

// writeBetaLocked : refus lisible sur les portes publiques (connexion, inscription).
func writeBetaLocked(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error": message,
		"code":  CodeBetaLocked,
	})
}

// CodeBetaLocked : code applicatif du refus. L’app iOS ne le connaît pas — elle se contente du
// message —, mais le site web s’en sert pour afficher un écran d’attente plutôt qu’une erreur.
const CodeBetaLocked = "beta_locked"
