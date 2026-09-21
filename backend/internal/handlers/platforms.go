package handlers

import (
	"context"
	"log"
	"net/http"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// AdminPlatforms GET /api/admin/platforms — d’où les comptes se connectent.
//
// Ce n’est pas un compteur de téléchargements : l’App Store ne dit à personne qui installe une
// app. On mesure ce qu’on observe réellement — les comptes qui ont ouvert une session depuis
// l’app iOS — et le rapport porte ses propres réserves (échantillons de libellés bruts, comptes
// sans session) pour qu’on ne lise pas ses chiffres pour plus qu’ils ne valent.
func (h *Handlers) AdminPlatforms(w http.ResponseWriter, r *http.Request) {
	report, err := h.db.PlatformsReport(r.Context())
	if err != nil {
		log.Printf("admin plateformes: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "plateformes"})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// notifyAdminsFirstIOSSession : une app iOS vient d’ouvrir sa première session sur ce compte.
//
// C’est le plus proche d’un « nouveau téléchargement » que le serveur puisse constater. Apple
// ne notifie aucune installation, et une app installée mais jamais ouverte reste invisible :
// cet évènement dit « quelqu’un a installé l’app et s’y est connecté », rien de plus.
//
// Tout se fait en tâche de fond, comme les autres notifications admin : une connexion ne doit
// pas ralentir ni échouer parce qu’Expo ou Mongo hoquette.
func (h *Handlers) notifyAdminsFirstIOSSession(u *models.User, device string, tokenID primitive.ObjectID) {
	if u == nil || !models.IsIOSDevice(device) {
		return
	}
	snapshot := *u
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()

		previous, err := h.db.ListDeviceLabelsForUser(ctx, snapshot.ID, tokenID)
		if err != nil {
			log.Printf("admin notif: sessions antérieures de %s: %v", snapshot.ID.Hex(), err)
			return
		}
		for _, d := range previous {
			if models.IsIOSDevice(d) {
				return // déjà installée sur ce compte : ce n’est pas une première.
			}
		}
		h.notifyAdmins(models.AdminEventIOSInstall, &snapshot, snapshot.EffectivePlan(), device)
	}()
}
