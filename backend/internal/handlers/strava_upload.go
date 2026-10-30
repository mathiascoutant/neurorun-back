package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
	"runapp/internal/strava"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Envoi des courses NeuroRun sur Strava. Il part du serveur, une fois la course
// enregistrée : la montre, le téléphone et la Garmin passent tous par
// POST /api/live-runs, et aucun n'a à connaître Strava.

const (
	// minStravaUploadM : un essai de quelques mètres n'a rien à faire sur le fil
	// Strava du coureur. Il reste publiable à la main depuis le détail.
	minStravaUploadM = 500
	// stravaUploadTimeout couvre la vérification, l'envoi et l'attente du traitement.
	stravaUploadTimeout = 3 * time.Minute
	// stravaDuplicateWindow : marge autour du départ pour reconnaître la même sortie
	// déjà arrivée sur Strava par Garmin Connect ou Santé.
	stravaDuplicateWindow = 10 * time.Minute
	stravaErrorMaxLen     = 300
)

// Codes lus par l'app pour savoir quoi proposer.
const stravaWriteMissingCode = "strava_write_missing"

// stravaUploadAllowed : compte lié, case « publier » laissée cochée, offre qui
// comprend Strava.
func (h *Handlers) stravaUploadAllowed(ctx context.Context, u *models.User) bool {
	if !u.HasStrava() || !u.Strava.CanUpload() {
		return false
	}
	ok, err := h.userHasCapability(ctx, u, "strava_dashboard")
	return err == nil && ok
}

// queueStravaUpload : envoi automatique d'une course qui vient d'arriver.
func (h *Handlers) queueStravaUpload(u *models.User, run *models.LiveRun) {
	if run.DistanceM < minStravaUploadM || !u.HasStrava() || !u.Strava.CanUpload() {
		return
	}
	userID, runID := u.ID, run.ID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), stravaUploadTimeout)
		defer cancel()
		fresh, err := h.db.FindUserByID(ctx, userID)
		if err != nil || !h.stravaUploadAllowed(ctx, fresh) {
			return
		}
		claimed, err := h.db.ClaimLiveRunStravaUpload(ctx, userID, runID, time.Now().UTC())
		if err != nil || !claimed {
			return
		}
		h.pushRunToStrava(ctx, fresh, runID)
	}()
}

// PushLiveRunToStrava POST /api/live-runs/{id}/strava — publie (ou republie après
// un échec) une course sur Strava. Répond tout de suite ; l'app relit la course.
func (h *Handlers) PushLiveRunToStrava(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "live_runs") {
		return
	}
	runID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	if !u.HasStrava() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "associe ton compte Strava", "code": stravaUnlinkedCode})
		return
	}
	if !h.stravaUploadAllowed(r.Context(), u) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "réassocie Strava en laissant cochée la publication des activités",
			"code":  stravaWriteMissingCode,
		})
		return
	}
	run, err := h.db.GetLiveRunMetaByID(r.Context(), runID)
	if err != nil || run.UserID != u.ID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "course introuvable"})
		return
	}
	if run.DistanceM <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "course vide"})
		return
	}
	now := time.Now().UTC()
	claimed, err := h.db.ClaimLiveRunStravaUpload(r.Context(), u.ID, runID, now)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "serveur"})
		return
	}
	if !claimed {
		// Déjà sur Strava, ou en train d'y partir : on rend l'état tel quel.
		writeJSON(w, http.StatusOK, map[string]any{"strava": run.Strava})
		return
	}
	userID := u.ID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), stravaUploadTimeout)
		defer cancel()
		fresh, err := h.db.FindUserByID(ctx, userID)
		if err != nil {
			return
		}
		h.pushRunToStrava(ctx, fresh, runID)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"strava": models.LiveRunStrava{Status: models.StravaUploadPending, UpdatedAt: now},
	})
}

// pushRunToStrava envoie une course déjà réservée (ClaimLiveRunStravaUpload) et
// note l'issue sur la course.
func (h *Handlers) pushRunToStrava(ctx context.Context, u *models.User, runID primitive.ObjectID) {
	activityID, err := h.uploadRunToStrava(ctx, u, runID)
	state := models.LiveRunStrava{Status: models.StravaUploadDone, ActivityID: activityID, UpdatedAt: time.Now().UTC()}
	if err != nil {
		log.Printf("strava: envoi de la course %s: %v", runID.Hex(), err)
		h.forgetStravaIfRevoked(ctx, u, err)
		state = models.LiveRunStrava{Status: models.StravaUploadFailed, Error: truncateErr(err), UpdatedAt: time.Now().UTC()}
	}
	// Contexte neuf : l'issue doit être notée même si l'attente a épuisé le délai,
	// sinon la course reste « en cours » jusqu'à ce que le verrou expire.
	saveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.db.SetLiveRunStrava(saveCtx, runID, state); err != nil {
		log.Printf("strava: état de la course %s: %v", runID.Hex(), err)
	}
}

func (h *Handlers) uploadRunToStrava(ctx context.Context, u *models.User, runID primitive.ObjectID) (int64, error) {
	run, err := h.db.GetLiveRunByUser(ctx, u.ID, runID)
	if err != nil {
		return 0, err
	}
	tok, err := h.ensureStravaAccess(ctx, u)
	if err != nil {
		return 0, err
	}

	// La montre a pu publier la sortie elle-même (Garmin Connect, Santé → Strava) :
	// on la rattache plutôt que d'en faire une seconde.
	mine := appLiveRunToRunActivity(run)
	if existing, err := h.strava.RunActivitiesBetween(ctx, tok,
		mine.StartAt.Add(-stravaDuplicateWindow), mine.StartAt.Add(stravaDuplicateWindow)); err == nil {
		for _, a := range existing {
			if nearSameRun(mine, a) {
				return a.ID, nil
			}
		}
	} else if errors.Is(err, strava.ErrUnauthorized) {
		return 0, err
	}

	name := stravaRunName(run)
	desc := stravaRunDescription(run)
	if !strava.HasTCXTrack(run) {
		loc, _ := calendarLocation()
		elapsed := int(run.WallSec)
		if elapsed < int(run.MovingSec) {
			elapsed = int(run.MovingSec)
		}
		return h.strava.CreateManualActivity(ctx, tok, strava.ManualActivity{
			Name:        name,
			Description: desc,
			StartLocal:  mine.StartAt.In(loc),
			ElapsedSec:  elapsed,
			DistanceM:   run.DistanceM,
		})
	}

	res, err := h.strava.UploadTCX(ctx, tok, strava.BuildTCX(run), name, desc, "neurorun-"+run.ID.Hex())
	if err == nil {
		res, err = h.strava.WaitUpload(ctx, tok, res)
	}
	var dup *strava.ErrUploadDuplicate
	if errors.As(err, &dup) {
		return dup.DuplicateOf, nil
	}
	if err != nil {
		return 0, err
	}
	return res.ActivityID, nil
}

// renameOnStrava reporte un nouveau nom sur l'activité Strava de la course, si elle
// y est. Un échec ne gêne pas le renommage côté NeuroRun.
func (h *Handlers) renameOnStrava(u *models.User, runID primitive.ObjectID) {
	if !u.HasStrava() || !u.Strava.CanUpload() {
		return
	}
	userID := u.ID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		run, err := h.db.GetLiveRunMetaByID(ctx, runID)
		if err != nil || run.UserID != userID || run.Strava == nil || run.Strava.ActivityID == 0 {
			return
		}
		fresh, err := h.db.FindUserByID(ctx, userID)
		if err != nil {
			return
		}
		tok, err := h.ensureStravaAccess(ctx, fresh)
		if err == nil {
			err = h.strava.RenameActivity(ctx, tok, run.Strava.ActivityID, stravaRunName(run))
		}
		if err != nil {
			log.Printf("strava: renommage de la course %s: %v", runID.Hex(), err)
			h.forgetStravaIfRevoked(ctx, fresh, err)
		}
	}()
}

// stravaRunName : le nom donné par le coureur, sinon le nom par défaut de l'app
// (src/lib/runName.ts), à l'heure de Paris.
func stravaRunName(run *models.LiveRun) string {
	if n := strings.TrimSpace(run.Name); n != "" {
		return n
	}
	loc, _ := calendarLocation()
	h := goalcalendar.LiveRunStart(*run).In(loc).Hour()
	switch {
	case h >= 5 && h < 11:
		return "Course matinale"
	case h >= 11 && h < 14:
		return "Course du midi"
	case h >= 14 && h < 18:
		return "Course de l’après-midi"
	case h >= 18 && h < 22:
		return "Course du soir"
	default:
		return "Course de nuit"
	}
}

// stravaRunDescription : la note et le type de séance, puis la signature.
func stravaRunDescription(run *models.LiveRun) string {
	var lines []string
	if run.Score != nil {
		lines = append(lines, fmt.Sprintf("Note NeuroRun : %d/100", run.Score.Total))
	}
	switch models.NormalizeRunType(run.SessionType) {
	case models.RunTypeEndurance:
		lines = append(lines, "Séance : endurance fondamentale")
	case models.RunTypeInterval:
		lines = append(lines, "Séance : fractionné")
	}
	lines = append(lines, "Enregistrée avec NeuroRun — neurorun.fr")
	return strings.Join(lines, "\n")
}

func truncateErr(err error) string {
	s := err.Error()
	if len([]rune(s)) > stravaErrorMaxLen {
		s = string([]rune(s)[:stravaErrorMaxLen]) + "…"
	}
	return s
}
