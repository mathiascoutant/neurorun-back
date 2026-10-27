package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"runapp/internal/models"
	"runapp/internal/shoes"
	"runapp/internal/store"
	"runapp/internal/strava"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	maxShoeTextLen = 40
	minLifespanKm  = 100.0
	maxLifespanKm  = 3000.0
	maxInitialKm   = 5000.0
	// pendingShoeWindow : au-delà, une course sans paire n'est plus demandée —
	// personne ne se souvient de ses chaussures d'il y a trois semaines.
	pendingShoeWindow = 14 * 24 * time.Hour
)

// shoeCategoryLabels : libellés des catégories, dans l'ordre d'affichage.
var shoeCategoryLabels = []struct{ ID, Label string }{
	{shoes.CategoryDaily, "Polyvalente"},
	{shoes.CategoryCushion, "Amorti max"},
	{shoes.CategoryStability, "Stabilité"},
	{shoes.CategoryTempo, "Dynamique"},
	{shoes.CategoryRace, "Compétition"},
	{shoes.CategoryTrail, "Trail"},
}

// shoeView : une paire telle que l'app l'affiche, usure comprise.
type shoeView struct {
	models.Shoe
	Retired bool       `json:"retired"`
	Runs    int        `json:"runs"`
	Wear    shoes.Wear `json:"wear"`
}

func buildShoeViews(list []models.Shoe, totals map[primitive.ObjectID]models.ShoeRunTotal, now time.Time) []shoeView {
	loc, _ := calendarLocation()
	out := make([]shoeView, 0, len(list))
	for _, s := range list {
		t := totals[s.ID]
		started, err := time.ParseInLocation("2006-01-02", s.StartedOn, loc)
		if err != nil {
			started = s.CreatedAt
		}
		out = append(out, shoeView{
			Shoe:    s,
			Retired: s.RetiredAt != nil,
			Runs:    t.Runs,
			Wear:    shoes.ComputeWear(s.InitialKm+t.DistanceM/1000, s.LifespanKm, started, now),
		})
	}
	// Paires actives d'abord ; l'ordre d'ajout départage.
	sort.SliceStable(out, func(i, j int) bool { return !out[i].Retired && out[j].Retired })
	return out
}

func (h *Handlers) loadShoeViews(ctx context.Context, userID primitive.ObjectID) ([]shoeView, error) {
	list, err := h.db.ListShoesByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	totals, err := h.db.ShoeRunTotals(ctx, userID)
	if err != nil {
		return nil, err
	}
	return buildShoeViews(list, totals, time.Now().UTC()), nil
}

func activeShoes(views []shoeView) []shoeView {
	out := []shoeView{}
	for _, v := range views {
		if !v.Retired {
			out = append(out, v)
		}
	}
	return out
}

// ListShoes GET /api/shoes — toutes les paires, avec leur usure.
func (h *Handlers) ListShoes(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	views, err := h.loadShoeViews(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "liste impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shoes": views})
}

// ShoeCatalog GET /api/shoes/catalog — modèles connus et catégories.
func (h *Handlers) ShoeCatalog(w http.ResponseWriter, _ *http.Request) {
	cats := make([]map[string]any, 0, len(shoeCategoryLabels))
	for _, c := range shoeCategoryLabels {
		cats = append(cats, map[string]any{
			"id":          c.ID,
			"label":       c.Label,
			"lifespan_km": shoes.LifespanFor(c.ID),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models":     shoes.Catalog(),
		"categories": cats,
	})
}

// parseStartedOn : jour civil d'achat, ni dans le futur ni absurde.
func parseStartedOn(raw string) (string, time.Time, bool) {
	loc, _ := calendarLocation()
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(raw), loc)
	if err != nil {
		return "", time.Time{}, false
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	if day.After(today) || day.Before(today.AddDate(-10, 0, 0)) {
		return "", time.Time{}, false
	}
	return day.Format("2006-01-02"), day, true
}

// ShoeHistoryKm GET /api/shoes/history-km?since=AAAA-MM-JJ — kilomètres courus
// depuis ce jour, courses NeuroRun et Strava confondues (sans doublon). Sert à
// préremplir les kilomètres déjà faits avec une paire qu'on ajoute.
func (h *Handlers) ShoeHistoryKm(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	_, since, ok := parseStartedOn(r.URL.Query().Get("since"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date invalide"})
		return
	}

	live, err := h.db.ListLiveRunsSince(r.Context(), u.ID, since)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "historique impossible"})
		return
	}
	after := since.Unix()
	var stravaRuns []strava.RunActivity
	stravaIncluded := false
	if u.HasStrava() {
		access, aerr := h.ensureStravaAccess(r.Context(), u)
		if aerr == nil {
			stravaRuns, aerr = h.strava.FetchRunActivities(r.Context(), access, &after)
			stravaIncluded = aerr == nil
		}
		h.forgetStravaIfRevoked(r.Context(), u, aerr)
	}

	runs := mergeStravaAndLiveRuns(stravaRuns, liveRunsToRunActivities(live, &after))
	var m float64
	for _, a := range runs {
		m += a.DistanceM
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"km":              math.Round(m/100) / 10,
		"runs":            len(runs),
		"strava_included": stravaIncluded,
	})
}

type shoeCreateBody struct {
	CatalogID  string   `json:"catalog_id"`
	Brand      string   `json:"brand"`
	Model      string   `json:"model"`
	Nickname   string   `json:"nickname"`
	Category   string   `json:"category"`
	LifespanKm *float64 `json:"lifespan_km"`
	StartedOn  string   `json:"started_on"`
	InitialKm  float64  `json:"initial_km"`
}

// cleanShoeText : espaces aplatis ; faux si trop long.
func cleanShoeText(raw string) (string, bool) {
	s := strings.Join(strings.Fields(raw), " ")
	return s, utf8.RuneCountInString(s) <= maxShoeTextLen
}

func validLifespan(km float64) bool {
	return km >= minLifespanKm && km <= maxLifespanKm && !math.IsNaN(km)
}

func validInitialKm(km float64) bool {
	return km >= 0 && km <= maxInitialKm && !math.IsNaN(km)
}

// CreateShoe POST /api/shoes — ajoute une paire. Un modèle du catalogue
// apporte marque, nom, catégorie et durée de vie ; une saisie libre prend la
// durée de vie de sa catégorie. lifespan_km, s'il est fourni, l'emporte.
func (h *Handlers) CreateShoe(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	var b shoeCreateBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corps invalide"})
		return
	}

	s := models.Shoe{UserID: u.ID}
	if id := strings.TrimSpace(b.CatalogID); id != "" {
		m, ok := shoes.Find(id)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "modèle inconnu"})
			return
		}
		s.CatalogID, s.Brand, s.Model, s.Category, s.LifespanKm = m.ID, m.Brand, m.Name, m.Category, m.LifespanKm
	} else {
		brand, okB := cleanShoeText(b.Brand)
		model, okM := cleanShoeText(b.Model)
		if !okB || !okM || brand == "" || model == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "marque et modèle requis (40 caractères max.)"})
			return
		}
		cat := strings.TrimSpace(b.Category)
		if cat == "" {
			cat = shoes.CategoryDaily
		}
		if !shoes.ValidCategory(cat) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "catégorie invalide"})
			return
		}
		s.Brand, s.Model, s.Category, s.LifespanKm = brand, model, cat, shoes.LifespanFor(cat)
	}
	if b.LifespanKm != nil {
		if !validLifespan(*b.LifespanKm) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "durée de vie entre 100 et 3000 km"})
			return
		}
		s.LifespanKm = math.Round(*b.LifespanKm)
	}
	nick, ok := cleanShoeText(b.Nickname)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "surnom trop long"})
		return
	}
	s.Nickname = nick
	day, _, ok := parseStartedOn(b.StartedOn)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date d'achat invalide"})
		return
	}
	s.StartedOn = day
	if !validInitialKm(b.InitialKm) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kilomètres déjà faits invalides"})
		return
	}
	s.InitialKm = math.Round(b.InitialKm*10) / 10

	existing, err := h.db.ListShoesByUser(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	if len(existing) >= models.MaxShoesPerUser {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "20 paires au maximum : supprime une ancienne paire"})
		return
	}
	if err := h.db.CreateShoe(r.Context(), &s); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	views := buildShoeViews([]models.Shoe{s}, nil, time.Now().UTC())
	writeJSON(w, http.StatusCreated, views[0])
}

type shoePatchBody struct {
	Nickname   *string  `json:"nickname"`
	LifespanKm *float64 `json:"lifespan_km"`
	InitialKm  *float64 `json:"initial_km"`
	StartedOn  *string  `json:"started_on"`
	Retired    *bool    `json:"retired"`
}

// PatchShoe PATCH /api/shoes/{id} — corrige une paire ou la met au placard.
func (h *Handlers) PatchShoe(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	shoeID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	var b shoePatchBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corps invalide"})
		return
	}
	set, unset := bson.M{}, bson.M{}
	if b.Nickname != nil {
		nick, ok := cleanShoeText(*b.Nickname)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "surnom trop long"})
			return
		}
		if nick == "" {
			unset["nickname"] = ""
		} else {
			set["nickname"] = nick
		}
	}
	if b.LifespanKm != nil {
		if !validLifespan(*b.LifespanKm) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "durée de vie entre 100 et 3000 km"})
			return
		}
		set["lifespan_km"] = math.Round(*b.LifespanKm)
	}
	if b.InitialKm != nil {
		if !validInitialKm(*b.InitialKm) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kilomètres déjà faits invalides"})
			return
		}
		set["initial_km"] = math.Round(*b.InitialKm*10) / 10
	}
	if b.StartedOn != nil {
		day, _, ok := parseStartedOn(*b.StartedOn)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date d'achat invalide"})
			return
		}
		set["started_on"] = day
	}
	if b.Retired != nil {
		if *b.Retired {
			set["retired_at"] = time.Now().UTC()
		} else {
			unset["retired_at"] = ""
		}
	}
	if err := h.db.UpdateShoe(r.Context(), u.ID, shoeID, set, unset); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "paire introuvable"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	h.writeShoe(w, r, u.ID, shoeID)
}

// writeShoe renvoie une paire à jour, usure recalculée.
func (h *Handlers) writeShoe(w http.ResponseWriter, r *http.Request, userID, shoeID primitive.ObjectID) {
	views, err := h.loadShoeViews(r.Context(), userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	for _, v := range views {
		if v.ID == shoeID {
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "paire introuvable"})
}

// DeleteShoe DELETE /api/shoes/{id}
func (h *Handlers) DeleteShoe(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	shoeID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	if err := h.db.DeleteShoeByUser(r.Context(), u.ID, shoeID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "paire introuvable"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression impossible"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pendingShoeSince : on ne demande que les courses postérieures à la première
// paire ajoutée, et des quinze derniers jours.
func pendingShoeSince(list []shoeView, now time.Time) time.Time {
	since := now.Add(-pendingShoeWindow)
	first := now
	for _, v := range list {
		if v.CreatedAt.Before(first) {
			first = v.CreatedAt
		}
	}
	if first.After(since) {
		return first
	}
	return since
}

// PendingShoeRun GET /api/shoes/pending-run — la dernière course terminée dont
// on ne sait pas encore avec quelle paire elle a été courue. L'app la montre
// en récapitulatif dès que le coureur revient, avec ses paires actives.
//
// Avec une seule paire active, il n'y a rien à demander : les courses en
// attente lui sont rattachées et la réponse est vide.
func (h *Handlers) PendingShoeRun(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	empty := map[string]any{"run": nil, "shoes": []shoeView{}, "remaining": 0}
	views, err := h.loadShoeViews(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	active := activeShoes(views)
	if len(active) == 0 {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	since := pendingShoeSince(views, time.Now().UTC())

	if len(active) == 1 {
		only := active[0].ID
		for i := 0; i < 20; i++ {
			run, err := h.db.PendingShoeRun(r.Context(), u.ID, since)
			if err != nil {
				break
			}
			if err := h.db.SetLiveRunShoe(r.Context(), u.ID, run.ID, &only); err != nil {
				break
			}
		}
		writeJSON(w, http.StatusOK, empty)
		return
	}

	run, err := h.db.PendingShoeRun(r.Context(), u.ID, since)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	remaining, _ := h.db.CountPendingShoeRuns(r.Context(), u.ID, since)

	startedAt := run.CreatedAt.UTC()
	if run.GpsStartTsMs > 0 {
		startedAt = time.UnixMilli(run.GpsStartTsMs).UTC()
	}
	recap := map[string]any{
		"id":                  run.ID.Hex(),
		"created_at":          run.CreatedAt.UTC().Format(time.RFC3339),
		"started_at":          startedAt.Format(time.RFC3339),
		"distance_m":          run.DistanceM,
		"moving_sec":          run.MovingSec,
		"avg_pace_sec_per_km": paceSecPerKm(run.DistanceM, run.MovingSec, run.AvgPaceSecPerKm),
		"session_type":        models.NormalizeRunType(run.SessionType),
	}
	if run.Name != "" {
		recap["name"] = run.Name
	}
	if run.Score != nil {
		recap["score_total"] = run.Score.Total
	}
	if run.ClientStats != nil && run.ClientStats.ElevationGainM > 0 {
		recap["elevation_gain_m"] = run.ClientStats.ElevationGainM
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run":       recap,
		"shoes":     active,
		"remaining": remaining,
	})
}

type liveRunShoeBody struct {
	// ShoeID : vide = « aucune de ces paires ».
	ShoeID string `json:"shoe_id"`
}

// SetLiveRunShoe PUT /api/live-runs/{id}/shoe {shoe_id} — paire portée pour
// cette course ; vide pour n'en rattacher aucune.
func (h *Handlers) SetLiveRunShoe(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	runID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	var b liveRunShoeBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corps invalide"})
		return
	}
	var shoeID *primitive.ObjectID
	if raw := strings.TrimSpace(b.ShoeID); raw != "" {
		id, err := primitive.ObjectIDFromHex(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "paire invalide"})
			return
		}
		if _, err := h.db.GetShoeByUser(r.Context(), u.ID, id); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "paire introuvable"})
			return
		}
		shoeID = &id
	}
	if err := h.db.SetLiveRunShoe(r.Context(), u.ID, runID, shoeID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "course introuvable"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	resp := map[string]any{"shoe_id": nil}
	if shoeID != nil {
		resp["shoe_id"] = shoeID.Hex()
	}
	writeJSON(w, http.StatusOK, resp)
}

// defaultShoeFor : la paire d'office d'une nouvelle course — la seule paire
// active du coureur. Avec zéro ou plusieurs paires, rien : l'app demandera.
func (h *Handlers) defaultShoeFor(ctx context.Context, userID primitive.ObjectID) *primitive.ObjectID {
	list, err := h.db.ListShoesByUser(ctx, userID)
	if err != nil {
		return nil
	}
	var only *primitive.ObjectID
	for i := range list {
		if list[i].RetiredAt != nil {
			continue
		}
		if only != nil {
			return nil
		}
		only = &list[i].ID
	}
	return only
}
