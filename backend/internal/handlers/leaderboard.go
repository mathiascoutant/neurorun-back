package handlers

import (
	"context"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"runapp/internal/goalcalendar"
	"runapp/internal/leaderboard"
	"runapp/internal/models"
	"runapp/internal/store"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// liveRunLeaderboard : points d'une course au moment où elle est enregistrée.
func liveRunLeaderboard(run *models.LiveRun) *models.LiveRunLeaderboard {
	var note *int
	if run.Score != nil && run.Score.Scorable {
		n := run.Score.Total
		note = &n
	}
	out := leaderboard.Points(leaderboard.Run{
		DistanceM:          run.DistanceM,
		MovingSec:          run.MovingSec,
		AvgPaceSecPerKm:    run.AvgPaceSecPerKm,
		MaxImpliedSpeedKmh: run.MaxImpliedSpeedKmh,
		Note:               note,
	})
	return &models.LiveRunLeaderboard{
		Points:  out.Points,
		Counted: out.Counted,
		Reason:  out.Reason,
		Km:      math.Round(run.DistanceM/10) / 100,
		Note:    note,
		At:      goalcalendar.LiveRunStart(*run),
	}
}

// BackfillLeaderboard : donne leurs points aux courses enregistrées avant les
// classements, avec leur note actuelle. Rien à faire aux démarrages suivants.
func (h *Handlers) BackfillLeaderboard(ctx context.Context) {
	done := 0
	for ctx.Err() == nil {
		runs, err := h.db.LiveRunsWithoutLeaderboard(ctx, 500)
		if err != nil {
			log.Printf("classement : rattrapage interrompu : %v", err)
			return
		}
		if len(runs) == 0 {
			break
		}
		for i := range runs {
			if err := h.db.SetLiveRunLeaderboard(ctx, runs[i].ID, liveRunLeaderboard(&runs[i])); err != nil {
				log.Printf("classement : rattrapage interrompu : %v", err)
				return
			}
		}
		done += len(runs)
	}
	if done > 0 {
		log.Printf("classement : %d courses rattrapées", done)
		h.leaderboardCache.clear()
	}
}

// leaderboardName : « Mathias C. ». Le nom complet ne sort pas du compte.
func leaderboardName(u *models.User) string {
	first := strings.TrimSpace(u.FirstName)
	last := strings.TrimSpace(u.LastName)
	if first == "" {
		first = "Coureur"
	}
	if r, _ := utf8.DecodeRuneInString(last); r != utf8.RuneError {
		return first + " " + strings.ToUpper(string(r)) + "."
	}
	return first
}

// rankedRunner : une ligne du classement Monde, avant filtrage par zone.
type rankedRunner struct {
	userID  primitive.ObjectID
	name    string
	country string
	total   store.LeaderboardTotal
}

type leaderboardEntry struct {
	at   time.Time
	rows []rankedRunner
}

// leaderboardCache : le classement complet d'une période, recalculé au plus toutes
// les deux minutes. Une nouvelle course ou un changement de pays le vide.
type leaderboardCache struct {
	mu      sync.Mutex
	entries map[string]leaderboardEntry
}

const leaderboardTTL = 2 * time.Minute

func (c *leaderboardCache) get(key string) ([]rankedRunner, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.at) > leaderboardTTL {
		return nil, false
	}
	return e.rows, true
}

func (c *leaderboardCache) put(key string, rows []rankedRunner) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]leaderboardEntry{}
	}
	c.entries[key] = leaderboardEntry{at: time.Now(), rows: rows}
}

func (c *leaderboardCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = nil
}

const (
	periodSeason = "season"
	periodAll    = "all"
	scopeWorld   = "world"
	scopeEurope  = "europe"
	scopeCountry = "country"
)

func periodSince(period string, now time.Time) *time.Time {
	if period == periodAll {
		return nil
	}
	s := leaderboard.SeasonStart(now)
	return &s
}

// rankedRunners : tous les coureurs classés d'une période, du premier au dernier.
// Les comptes retirés des classements n'y sont pas.
func (h *Handlers) rankedRunners(ctx context.Context, period string, now time.Time) ([]rankedRunner, error) {
	key := period
	if period == periodSeason {
		key += leaderboard.SeasonStart(now).Format("2006-01")
	}
	if rows, ok := h.leaderboardCache.get(key); ok {
		return rows, nil
	}
	totals, err := h.db.LeaderboardTotals(ctx, periodSince(period, now))
	if err != nil {
		return nil, err
	}
	ids := make([]primitive.ObjectID, 0, len(totals))
	for _, t := range totals {
		ids = append(ids, t.UserID)
	}
	users, err := h.db.LeaderboardUsers(ctx, ids)
	if err != nil {
		return nil, err
	}
	rows := make([]rankedRunner, 0, len(totals))
	for _, t := range totals {
		u, ok := users[t.UserID]
		if !ok || u.LeaderboardHidden || t.Points <= 0 {
			continue
		}
		rows = append(rows, rankedRunner{userID: u.ID, name: leaderboardName(&u), country: u.Country, total: t})
	}
	sortRunners(rows)
	h.leaderboardCache.put(key, rows)
	return rows, nil
}

// sortRunners : points d'abord, puis kilomètres ; l'identifiant départage pour que
// l'ordre ne change pas d'un appel à l'autre.
func sortRunners(rows []rankedRunner) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].total, rows[j].total
		if a.Points != b.Points {
			return a.Points > b.Points
		}
		if a.Km != b.Km {
			return a.Km > b.Km
		}
		return rows[i].userID.Hex() < rows[j].userID.Hex()
	})
}

// inScope : la ligne appartient à la zone demandée. Un compte sans pays n'est
// qu'au classement Monde.
func inScope(country, scope, viewerCountry string) bool {
	switch scope {
	case scopeEurope:
		return leaderboard.IsEurope(country)
	case scopeCountry:
		return country != "" && country == viewerCountry
	default:
		return true
	}
}

// rankRows : rangs de la zone, ex æquo au même rang (1, 2, 2, 4).
func rankRows(rows []rankedRunner, scope, viewerCountry string) ([]rankedRunner, []int) {
	out := make([]rankedRunner, 0, len(rows))
	ranks := make([]int, 0, len(rows))
	for _, r := range rows {
		if !inScope(r.country, scope, viewerCountry) {
			continue
		}
		rank := len(out) + 1
		if n := len(out); n > 0 && out[n-1].total.Points == r.total.Points {
			rank = ranks[n-1]
		}
		out = append(out, r)
		ranks = append(ranks, rank)
	}
	return out, ranks
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func runnerRow(r rankedRunner, rank int, me primitive.ObjectID) map[string]any {
	return map[string]any{
		"rank":   rank,
		"user":   map[string]any{"id": r.userID.Hex(), "name": r.name, "country": r.country},
		"points": round1(r.total.Points),
		"km":     round1(r.total.Km),
		"runs":   r.total.Runs,
		"is_me":  r.userID == me,
	}
}

func seasonJSON(now time.Time) map[string]any {
	return map[string]any{
		"start": leaderboard.SeasonStart(now).UTC().Format(time.RFC3339),
		"end":   leaderboard.SeasonEnd(now).UTC().Format(time.RFC3339),
	}
}

// Leaderboard GET /api/leaderboard?scope=world|europe|country&period=season|all&offset=&limit=
// La ligne du coureur revient toujours à part (`me`), même loin du haut du tableau.
func (h *Handlers) Leaderboard(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	q := r.URL.Query()
	scope := q.Get("scope")
	if scope != scopeEurope && scope != scopeCountry {
		scope = scopeWorld
	}
	period := q.Get("period")
	if period != periodAll {
		period = periodSeason
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	now := time.Now()
	all, err := h.rankedRunners(r.Context(), period, now)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	rows, ranks := rankRows(all, scope, u.Country)

	page := make([]map[string]any, 0, limit)
	for i := offset; i < len(rows) && i < offset+limit; i++ {
		page = append(page, runnerRow(rows[i], ranks[i], u.ID))
	}
	var me map[string]any
	for i := range rows {
		if rows[i].userID == u.ID {
			me = runnerRow(rows[i], ranks[i], u.ID)
			break
		}
	}

	out := map[string]any{
		"scope":    scope,
		"period":   period,
		"country":  u.Country,
		"total":    len(rows),
		"offset":   offset,
		"rows":     page,
		"me":       me,
		"hidden":   u.LeaderboardHidden,
		"season":   seasonJSON(now),
		"has_more": offset+limit < len(rows),
	}
	writeJSON(w, http.StatusOK, out)
}

// rankOf : rang d'un coureur dans une zone, 0 s'il n'y figure pas.
func rankOf(all []rankedRunner, id primitive.ObjectID, scope, country string) int {
	rows, ranks := rankRows(all, scope, country)
	for i := range rows {
		if rows[i].userID == id {
			return ranks[i]
		}
	}
	return 0
}

// LeaderboardRunner GET /api/leaderboard/users/{id} — fiche d'un coureur : chiffres
// cumulés seulement, jamais de tracé, de lieu ni d'heure de course.
func (h *Handlers) LeaderboardRunner(w http.ResponseWriter, r *http.Request) {
	viewer := r.Context().Value(ctxUser{}).(*models.User)
	id, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	users, err := h.db.LeaderboardUsers(r.Context(), []primitive.ObjectID{id})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	u, ok := users[id]
	// Retiré des classements : sa fiche n'est visible que de lui-même.
	if !ok || (u.LeaderboardHidden && id != viewer.ID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "coureur introuvable"})
		return
	}

	now := time.Now()
	allTime, err := h.db.LeaderboardTotalForUser(r.Context(), id, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	season, err := h.db.LeaderboardTotalForUser(r.Context(), id, periodSince(periodSeason, now))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	seasonRows, err := h.rankedRunners(r.Context(), periodSeason, now)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	allRows, err := h.rankedRunners(r.Context(), periodAll, now)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}

	var avg any
	if a := allTime.AvgNote(); a != nil {
		avg = round1(*a)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":  map[string]any{"id": id.Hex(), "name": leaderboardName(&u), "country": u.Country},
		"is_me": id == viewer.ID,
		"season": map[string]any{
			"points":       round1(season.Points),
			"km":           round1(season.Km),
			"runs":         season.Runs,
			"rank_world":   rankOf(seasonRows, id, scopeWorld, u.Country),
			"rank_country": rankOf(seasonRows, id, scopeCountry, u.Country),
		},
		"all_time": map[string]any{
			"points":       round1(allTime.Points),
			"km":           round1(allTime.Km),
			"runs":         allTime.Runs,
			"avg_note":     avg,
			"longest_km":   round1(allTime.LongestKm),
			"rank_world":   rankOf(allRows, id, scopeWorld, u.Country),
			"rank_country": rankOf(allRows, id, scopeCountry, u.Country),
		},
	})
}
