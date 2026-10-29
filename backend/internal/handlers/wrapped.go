package handlers

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"runapp/internal/models"
	"runapp/internal/store"
	"runapp/internal/strava"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// « NeuroRun Wrapped » : le récap d'une année de course, ou d'une préparation
// d'objectif, raconté en diapos par l'app. Le serveur fait tous les calculs —
// l'app ne fait que mettre en scène — pour que le web et le mobile racontent la
// même histoire.
//
// Sources : les courses NeuroRun, plus les sorties Strava quand le compte est
// lié. Une sortie enregistrée dans NeuroRun puis retrouvée sur Strava ne compte
// qu'une fois, et c'est la version NeuroRun qui est gardée : elle seule porte la
// note, le type de séance et la paire de chaussures.

// Distance minimale d'une course retenue : en deçà, c'est un départ avorté.
const wrappedMinRunM = 100.0

// Allure plancher (s/km) d'une « meilleure allure » : plus rapide que 2:30/km
// sur 5 km, c'est un GPS qui a sauté, pas un record.
const wrappedMinPaceSecPerKm = 150.0

// Distance minimale (m) pour la meilleure allure.
const wrappedFastestMinM = 5000.0

// wrappedRun : une sortie, quelle que soit sa source, réduite à ce que le récap lit.
type wrappedRun struct {
	Source      string // "live" ou "strava"
	ID          string
	Name        string
	StartAt     time.Time
	DistanceM   float64
	MovingSec   float64
	ElevGainM   float64
	SessionType string // vide pour Strava : le type n'y est pas choisi
	IsInterval  bool
	Score       *int
	ShoeID      *primitive.ObjectID
	HasStart    bool
	StartLat    float64
	StartLng    float64
	Polyline    string
}

func (r *wrappedRun) paceSecPerKm() float64 {
	if r.DistanceM <= 0 || r.MovingSec <= 0 {
		return 0
	}
	return r.MovingSec / (r.DistanceM / 1000)
}

// wrappedPeriod : la fenêtre racontée, en heure de Paris. To est exclu.
type wrappedPeriod struct {
	Kind      string // "year" ou "goal"
	Year      int
	GoalID    string
	GoalTitle string
	Terrain   string
	From      time.Time
	To        time.Time
	Loc       *time.Location
	Now       time.Time
}

// ——— Réponse JSON ———

type wrappedPeriodOut struct {
	Kind       string `json:"kind"`
	Year       int    `json:"year,omitempty"`
	GoalID     string `json:"goal_id,omitempty"`
	GoalTitle  string `json:"goal_title,omitempty"`
	Terrain    string `json:"terrain,omitempty"`
	From       string `json:"from"` // AAAA-MM-JJ
	To         string `json:"to"`   // AAAA-MM-JJ, dernier jour inclus
	InProgress bool   `json:"in_progress"`
}

type wrappedSourcesOut struct {
	NeurorunRuns   int  `json:"neurorun_runs"`
	StravaRuns     int  `json:"strava_runs"`
	StravaIncluded bool `json:"strava_included"`
}

type wrappedTotalsOut struct {
	DistanceKm      float64 `json:"distance_km"`
	Runs            int     `json:"runs"`
	MovingSec       float64 `json:"moving_sec"`
	ElevationGainM  float64 `json:"elevation_gain_m"`
	ActiveDays      int     `json:"active_days"`
	ActiveWeeks     int     `json:"active_weeks"`
	AvgPaceSecPerKm float64 `json:"avg_pace_sec_per_km"`
	AvgDistanceKm   float64 `json:"avg_distance_km"`
	StartPlaces     int     `json:"start_places"`
}

type wrappedBucketOut struct {
	Label      string  `json:"label"`
	LongLabel  string  `json:"long_label"`
	Start      string  `json:"start"` // AAAA-MM-JJ
	DistanceKm float64 `json:"distance_km"`
	Runs       int     `json:"runs"`
}

type wrappedSeriesOut struct {
	Unit    string             `json:"unit"` // "month" ou "week"
	Buckets []wrappedBucketOut `json:"buckets"`
	// BestIndex : rang du mois (ou de la semaine) le plus chargé, -1 sans course.
	BestIndex int `json:"best_index"`
}

type wrappedRunOut struct {
	Source          string  `json:"source"`
	ID              string  `json:"id"`
	Name            string  `json:"name,omitempty"`
	StartAt         string  `json:"start_at"`
	DistanceKm      float64 `json:"distance_km"`
	MovingSec       float64 `json:"moving_sec"`
	AvgPaceSecPerKm float64 `json:"avg_pace_sec_per_km"`
	ElevationGainM  float64 `json:"elevation_gain_m,omitempty"`
	SessionType     string  `json:"session_type,omitempty"`
	ScoreTotal      *int    `json:"score_total,omitempty"`
	RoutePolyline   string  `json:"route_polyline,omitempty"`
}

type wrappedSessionTypesOut struct {
	Free      int `json:"free"`
	Endurance int `json:"endurance"`
	Interval  int `json:"interval"`
	// Total : courses NeuroRun seules — sur Strava le type n'est pas choisi.
	Total int `json:"total"`
}

type wrappedTimeOfDayOut struct {
	Morning   int `json:"morning"`   // 5 h – 11 h
	Noon      int `json:"noon"`      // 11 h – 14 h
	Afternoon int `json:"afternoon"` // 14 h – 18 h
	Evening   int `json:"evening"`   // 18 h – 5 h
	// Favorite : "morning", "noon", "afternoon" ou "evening" ; vide sans course.
	Favorite     string `json:"favorite,omitempty"`
	FavoriteHour int    `json:"favorite_hour"`
	EarlyRuns    int    `json:"early_runs"` // départ entre 4 h et 8 h
	LateRuns     int    `json:"late_runs"`  // départ à 20 h ou plus tard
}

type wrappedStreakOut struct {
	Weeks int    `json:"weeks"`
	From  string `json:"from,omitempty"` // lundi de la première semaine
	To    string `json:"to,omitempty"`   // dimanche de la dernière
}

type wrappedShoeOut struct {
	ID         string  `json:"id"`
	Brand      string  `json:"brand"`
	Model      string  `json:"model"`
	Nickname   string  `json:"nickname,omitempty"`
	DistanceKm float64 `json:"distance_km"`
	Runs       int     `json:"runs"`
}

type wrappedBoosterOut struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Boosts      int    `json:"boosts"`
}

type wrappedSocialOut struct {
	BoostsReceived int                `json:"boosts_received"`
	BoostsSent     int                `json:"boosts_sent"`
	TopBooster     *wrappedBoosterOut `json:"top_booster,omitempty"`
}

type wrappedProfileOut struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Tagline string `json:"tagline"`
}

type wrappedOut struct {
	Period       wrappedPeriodOut       `json:"period"`
	Sources      wrappedSourcesOut      `json:"sources"`
	Totals       wrappedTotalsOut       `json:"totals"`
	Series       wrappedSeriesOut       `json:"series"`
	LongestRun   *wrappedRunOut         `json:"longest_run"`
	FastestRun   *wrappedRunOut         `json:"fastest_run"`
	BestScore    *wrappedRunOut         `json:"best_score"`
	SessionTypes wrappedSessionTypesOut `json:"session_types"`
	TimeOfDay    wrappedTimeOfDayOut    `json:"time_of_day"`
	WeekStreak   wrappedStreakOut       `json:"week_streak"`
	TopShoe      *wrappedShoeOut        `json:"top_shoe"`
	Social       wrappedSocialOut       `json:"social"`
	Profile      *wrappedProfileOut     `json:"profile"`

	// Champs internes : identifiants à compléter par le handler (trace, paire).
	topShoeID primitive.ObjectID
}

// ——— Handler ———

// Wrapped GET /api/wrapped?year=2026 | ?goal_id=<id> — le récap. Sans
// paramètre : l'année en cours, à l'heure de Paris.
func (h *Handlers) Wrapped(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	loc, _ := calendarLocation()
	now := time.Now().In(loc)

	var p wrappedPeriod
	if goalHex := strings.TrimSpace(r.URL.Query().Get("goal_id")); goalHex != "" {
		oid, err := primitive.ObjectIDFromHex(goalHex)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "goal_id invalide"})
			return
		}
		g, err := h.db.GetGoalByUser(r.Context(), u.ID, oid)
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "objectif introuvable"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
			return
		}
		p = goalWrappedPeriod(g, loc, now)
	} else {
		year := now.Year()
		if v := strings.TrimSpace(r.URL.Query().Get("year")); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 2010 || n > now.Year() {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "année invalide"})
				return
			}
			year = n
		}
		p = yearWrappedPeriod(year, loc, now)
	}

	// Une course de la montre arrive en base après coup : on élargit la
	// recherche de deux jours, puis on filtre sur l'heure de départ.
	live, err := h.db.ListLiveRunsForWrapped(r.Context(), u.ID, p.From, p.To.Add(48*time.Hour))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "courses illisibles"})
		return
	}
	runs := make([]wrappedRun, 0, len(live))
	for i := range live {
		runs = append(runs, wrappedRunFromLive(&live[i]))
	}

	stravaIncluded := false
	if u.HasStrava() {
		access, aerr := h.ensureStravaAccess(r.Context(), u)
		if aerr == nil {
			after := p.From.Unix()
			var acts []strava.RunActivity
			acts, aerr = h.strava.FetchRunActivities(r.Context(), access, &after)
			if aerr == nil {
				stravaIncluded = true
				ext := make([]wrappedRun, 0, len(acts))
				for i := range acts {
					ext = append(ext, wrappedRunFromStrava(&acts[i]))
				}
				runs = mergeWrappedRuns(runs, ext)
			}
		}
		h.forgetStravaIfRevoked(r.Context(), u, aerr)
	}

	runs = filterWrappedRuns(runs, p)
	out := computeWrapped(runs, p)
	out.Sources.StravaIncluded = stravaIncluded

	// Silhouettes : seules les courses mises en avant chargent leur trace.
	for _, ro := range []*wrappedRunOut{out.BestScore, out.LongestRun} {
		if ro == nil || ro.Source != "live" || ro.RoutePolyline != "" {
			continue
		}
		if oid, err := primitive.ObjectIDFromHex(ro.ID); err == nil {
			if full, err := h.db.GetLiveRunByUser(r.Context(), u.ID, oid); err == nil {
				ro.RoutePolyline = liveRunRoutePolyline(full.TrackPoints)
			}
		}
	}

	if !out.topShoeID.IsZero() && out.TopShoe != nil {
		if list, err := h.db.ListShoesByUser(r.Context(), u.ID); err == nil {
			for i := range list {
				if list[i].ID == out.topShoeID {
					out.TopShoe.Brand = list[i].Brand
					out.TopShoe.Model = list[i].Model
					out.TopShoe.Nickname = list[i].Nickname
					break
				}
			}
		}
		// Paire supprimée depuis : rien à montrer.
		if out.TopShoe.Model == "" {
			out.TopShoe = nil
		}
	}

	if boosts, err := h.db.BoostStatsBetween(r.Context(), u.ID, p.From, p.To); err == nil {
		out.Social = h.wrappedSocial(r, boosts)
	}

	writeJSON(w, http.StatusOK, out)
}

// wrappedSocial : totaux et l'ami qui a le plus boosté (prénom seul).
func (h *Handlers) wrappedSocial(r *http.Request, b store.WrappedBoosts) wrappedSocialOut {
	out := wrappedSocialOut{BoostsSent: b.Sent}
	var topID primitive.ObjectID
	top := 0
	for id, n := range b.Received {
		out.BoostsReceived += n
		// Égalité : l'identifiant le plus petit, pour un résultat stable.
		if n > top || (n == top && id.Hex() < topID.Hex()) {
			top, topID = n, id
		}
	}
	if top > 0 {
		if users := h.usersByID(r.Context(), []primitive.ObjectID{topID}); users[topID] != nil {
			out.TopBooster = &wrappedBoosterOut{
				ID:          topID.Hex(),
				DisplayName: friendDisplayName(users[topID]),
				Boosts:      top,
			}
		}
	}
	return out
}

// ——— Périodes ———

func yearWrappedPeriod(year int, loc *time.Location, now time.Time) wrappedPeriod {
	return wrappedPeriod{
		Kind: "year",
		Year: year,
		From: time.Date(year, 1, 1, 0, 0, 0, 0, loc),
		To:   time.Date(year+1, 1, 1, 0, 0, 0, 0, loc),
		Loc:  loc,
		Now:  now,
	}
}

// goalWrappedPeriod : de la création de l'objectif à sa fin — le lendemain du
// jour de la course s'il est connu, sinon création + semaines du plan.
func goalWrappedPeriod(g *models.Goal, loc *time.Location, now time.Time) wrappedPeriod {
	created := g.CreatedAt.In(loc)
	from := time.Date(created.Year(), created.Month(), created.Day(), 0, 0, 0, 0, loc)
	weeks := g.Weeks
	if weeks < 1 {
		weeks = 1
	}
	to := created.AddDate(0, 0, 7*weeks)
	if d, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(g.RaceDate), loc); err == nil && d.After(from) {
		to = d.AddDate(0, 0, 1)
	}
	title := strings.TrimSpace(g.CustomTitle)
	if title == "" {
		title = g.DistanceLabel
	}
	return wrappedPeriod{
		Kind:      "goal",
		GoalID:    g.ID.Hex(),
		GoalTitle: title,
		Terrain:   g.EffectiveTerrain(),
		From:      from,
		To:        to,
		Loc:       loc,
		Now:       now,
	}
}

// ——— Sources ———

func wrappedRunFromLive(lr *models.LiveRun) wrappedRun {
	start := lr.CreatedAt.Add(-time.Duration(lr.WallSec * float64(time.Second)))
	if lr.GpsStartTsMs > 0 {
		if t := time.UnixMilli(lr.GpsStartTsMs); t.Before(lr.CreatedAt.Add(time.Minute)) {
			start = t
		}
	}
	run := wrappedRun{
		Source:      "live",
		ID:          lr.ID.Hex(),
		Name:        lr.Name,
		StartAt:     start.UTC(),
		DistanceM:   lr.DistanceM,
		MovingSec:   lr.MovingSec,
		SessionType: models.NormalizeRunType(lr.SessionType),
		IsInterval:  lr.SessionType == models.RunTypeInterval,
		ShoeID:      lr.ShoeID,
	}
	if run.MovingSec <= 0 && lr.AvgPaceSecPerKm > 0 {
		run.MovingSec = lr.AvgPaceSecPerKm * lr.DistanceM / 1000
	}
	if lr.ClientStats != nil {
		run.ElevGainM = lr.ClientStats.ElevationGainM
	}
	if lr.Score != nil && lr.Score.Scorable {
		total := lr.Score.Total
		run.Score = &total
	}
	if len(lr.TrackPoints) > 0 {
		p := lr.TrackPoints[0]
		if p.Lat != 0 || p.Lng != 0 {
			run.HasStart, run.StartLat, run.StartLng = true, p.Lat, p.Lng
		}
	}
	return run
}

func wrappedRunFromStrava(a *strava.RunActivity) wrappedRun {
	run := wrappedRun{
		Source:     "strava",
		ID:         strconv.FormatInt(a.ID, 10),
		Name:       a.Name,
		StartAt:    a.StartAt.UTC(),
		DistanceM:  a.DistanceM,
		MovingSec:  float64(a.MovingSec),
		ElevGainM:  a.ElevGainM,
		IsInterval: strava.IsIntervalWorkout(a.WorkoutType, a.Name),
		Polyline:   a.SummaryPolyline,
	}
	if lat, lng, ok := firstPolylinePoint(a.SummaryPolyline); ok {
		run.HasStart, run.StartLat, run.StartLng = true, lat, lng
	}
	return run
}

// mergeWrappedRuns ajoute les sorties Strava qui ne doublonnent pas une course
// NeuroRun (même règle que le tableau de bord : départ à 3 min près, distance
// à 400 m près). Sur un doublon, la version NeuroRun reste et récupère au
// besoin le dénivelé de Strava.
func mergeWrappedRuns(live, ext []wrappedRun) []wrappedRun {
	out := make([]wrappedRun, 0, len(live)+len(ext))
	out = append(out, live...)
	liveCount := len(out)
next:
	for _, s := range ext {
		for i := 0; i < liveCount; i++ {
			l := &out[i]
			if math.Abs(l.StartAt.Sub(s.StartAt).Seconds()) <= 180 && math.Abs(l.DistanceM-s.DistanceM) < 400 {
				if l.ElevGainM <= 0 && s.ElevGainM > 0 {
					l.ElevGainM = s.ElevGainM
				}
				if !l.HasStart && s.HasStart {
					l.HasStart, l.StartLat, l.StartLng = true, s.StartLat, s.StartLng
				}
				continue next
			}
		}
		out = append(out, s)
	}
	return out
}

// filterWrappedRuns : sorties parties dans la période, départs avortés écartés,
// de la plus ancienne à la plus récente.
func filterWrappedRuns(runs []wrappedRun, p wrappedPeriod) []wrappedRun {
	out := make([]wrappedRun, 0, len(runs))
	for _, r := range runs {
		if r.DistanceM < wrappedMinRunM || r.StartAt.Before(p.From) || !r.StartAt.Before(p.To) {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartAt.Before(out[j].StartAt) })
	return out
}

// firstPolylinePoint lit le premier point d'une polyline Google (le départ).
func firstPolylinePoint(s string) (lat, lng float64, ok bool) {
	idx := 0
	read := func() (int64, bool) {
		var result int64
		var shift uint
		for idx < len(s) {
			b := int64(s[idx]) - 63
			idx++
			result |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				if result&1 != 0 {
					return ^(result >> 1), true
				}
				return result >> 1, true
			}
		}
		return 0, false
	}
	la, ok1 := read()
	ln, ok2 := read()
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	lat, lng = float64(la)/1e5, float64(ln)/1e5
	if lat == 0 && lng == 0 {
		return 0, 0, false
	}
	return lat, lng, true
}

// ——— Calcul (pur, testé) ———

var frMonthShort = [12]string{"janv.", "févr.", "mars", "avr.", "mai", "juin", "juil.", "août", "sept.", "oct.", "nov.", "déc."}
var frMonthLong = [12]string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"}

func wrRound1(x float64) float64 { return math.Round(x*10) / 10 }
func wrRound2(x float64) float64 { return math.Round(x*100) / 100 }

func wrDayKey(t time.Time) string { return t.Format("2006-01-02") }

// wrMondayOf : lundi 00:00 de la semaine de t, dans le fuseau de t.
func wrMondayOf(t time.Time) time.Time {
	offset := (int(t.Weekday()) + 6) % 7
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).AddDate(0, 0, -offset)
}

// wrappedBuckets : douze mois pour une année, des semaines depuis le premier
// jour pour un objectif (au plus 60).
func wrappedBuckets(p wrappedPeriod) (unit string, starts []time.Time) {
	if p.Kind == "year" {
		for m := 0; m < 12; m++ {
			starts = append(starts, time.Date(p.Year, time.Month(m+1), 1, 0, 0, 0, 0, p.Loc))
		}
		return "month", starts
	}
	for t := p.From; t.Before(p.To) && len(starts) < 60; t = t.AddDate(0, 0, 7) {
		starts = append(starts, t)
	}
	if len(starts) == 0 {
		starts = append(starts, p.From)
	}
	return "week", starts
}

func toWrappedRunOut(r *wrappedRun, loc *time.Location) *wrappedRunOut {
	return &wrappedRunOut{
		Source:          r.Source,
		ID:              r.ID,
		Name:            r.Name,
		StartAt:         r.StartAt.In(loc).Format(time.RFC3339),
		DistanceKm:      wrRound2(r.DistanceM / 1000),
		MovingSec:       math.Round(r.MovingSec),
		AvgPaceSecPerKm: math.Round(r.paceSecPerKm()),
		ElevationGainM:  math.Round(r.ElevGainM),
		SessionType:     r.SessionType,
		ScoreTotal:      r.Score,
		RoutePolyline:   r.Polyline,
	}
}

// computeWrapped : tous les chiffres du récap à partir des sorties de la
// période (déjà filtrées et dédoublonnées). Ne touche ni la base ni le réseau.
func computeWrapped(runs []wrappedRun, p wrappedPeriod) wrappedOut {
	loc := p.Loc
	lastDay := p.To.AddDate(0, 0, -1)
	out := wrappedOut{
		Period: wrappedPeriodOut{
			Kind:       p.Kind,
			Year:       p.Year,
			GoalID:     p.GoalID,
			GoalTitle:  p.GoalTitle,
			Terrain:    p.Terrain,
			From:       wrDayKey(p.From.In(loc)),
			To:         wrDayKey(lastDay.In(loc)),
			InProgress: p.Now.Before(p.To),
		},
	}

	unit, starts := wrappedBuckets(p)
	buckets := make([]wrappedBucketOut, len(starts))
	bucketM := make([]float64, len(starts))
	for i, s := range starts {
		b := wrappedBucketOut{Start: wrDayKey(s)}
		if unit == "month" {
			b.Label, b.LongLabel = frMonthShort[s.Month()-1], frMonthLong[s.Month()-1]
		} else {
			b.Label, b.LongLabel = fmt.Sprintf("S%d", i+1), fmt.Sprintf("semaine %d", i+1)
		}
		buckets[i] = b
	}

	var totalM, totalSec, paceM, paceSec, elev float64
	days := map[string]bool{}
	weeks := map[string]time.Time{}
	places := map[[2]int]bool{}
	hours := [24]int{}
	shoeM := map[primitive.ObjectID]float64{}
	shoeRuns := map[primitive.ObjectID]int{}
	var longest, fastest, best *wrappedRun

	for i := range runs {
		r := &runs[i]
		local := r.StartAt.In(loc)
		totalM += r.DistanceM
		totalSec += r.MovingSec
		elev += math.Max(r.ElevGainM, 0)
		if r.MovingSec > 0 {
			paceM += r.DistanceM
			paceSec += r.MovingSec
		}
		days[wrDayKey(local)] = true
		mon := wrMondayOf(local)
		weeks[wrDayKey(mon)] = mon
		if r.HasStart {
			// Maille d'environ un kilomètre : deux départs de la même rue
			// comptent pour un seul lieu.
			places[[2]int{int(math.Round(r.StartLat * 100)), int(math.Round(r.StartLng * 100))}] = true
		}

		// Seau : dernier début de seau atteint par le départ.
		for b := len(starts) - 1; b >= 0; b-- {
			if !local.Before(starts[b]) {
				bucketM[b] += r.DistanceM
				buckets[b].Runs++
				break
			}
		}

		h := local.Hour()
		hours[h]++
		switch {
		case h >= 5 && h < 11:
			out.TimeOfDay.Morning++
		case h >= 11 && h < 14:
			out.TimeOfDay.Noon++
		case h >= 14 && h < 18:
			out.TimeOfDay.Afternoon++
		default:
			out.TimeOfDay.Evening++
		}
		if h >= 4 && h < 8 {
			out.TimeOfDay.EarlyRuns++
		}
		if h >= 20 || h < 4 {
			out.TimeOfDay.LateRuns++
		}

		if r.Source == "live" {
			out.Sources.NeurorunRuns++
			out.SessionTypes.Total++
			switch r.SessionType {
			case models.RunTypeEndurance:
				out.SessionTypes.Endurance++
			case models.RunTypeInterval:
				out.SessionTypes.Interval++
			default:
				out.SessionTypes.Free++
			}
		} else {
			out.Sources.StravaRuns++
		}

		if r.ShoeID != nil && !r.ShoeID.IsZero() {
			shoeM[*r.ShoeID] += r.DistanceM
			shoeRuns[*r.ShoeID]++
		}

		if longest == nil || r.DistanceM > longest.DistanceM {
			longest = r
		}
		if pace := r.paceSecPerKm(); r.DistanceM >= wrappedFastestMinM && pace >= wrappedMinPaceSecPerKm &&
			(fastest == nil || pace < fastest.paceSecPerKm()) {
			fastest = r
		}
		if r.Score != nil && (best == nil || *r.Score > *best.Score ||
			(*r.Score == *best.Score && r.DistanceM > best.DistanceM)) {
			best = r
		}
	}

	out.Series = wrappedSeriesOut{Unit: unit, BestIndex: -1}
	bestM := 0.0
	for i := range buckets {
		buckets[i].DistanceKm = wrRound1(bucketM[i] / 1000)
		if bucketM[i] > bestM {
			bestM, out.Series.BestIndex = bucketM[i], i
		}
	}
	out.Series.Buckets = buckets

	out.Totals = wrappedTotalsOut{
		DistanceKm:     wrRound1(totalM / 1000),
		Runs:           len(runs),
		MovingSec:      math.Round(totalSec),
		ElevationGainM: math.Round(elev),
		ActiveDays:     len(days),
		ActiveWeeks:    len(weeks),
		StartPlaces:    len(places),
	}
	if paceM > 0 {
		out.Totals.AvgPaceSecPerKm = math.Round(paceSec / (paceM / 1000))
	}
	if len(runs) > 0 {
		out.Totals.AvgDistanceKm = wrRound1(totalM / 1000 / float64(len(runs)))
	}

	if longest != nil {
		out.LongestRun = toWrappedRunOut(longest, loc)
	}
	if fastest != nil {
		out.FastestRun = toWrappedRunOut(fastest, loc)
	}
	if best != nil {
		out.BestScore = toWrappedRunOut(best, loc)
	}

	if len(runs) > 0 {
		tod := &out.TimeOfDay
		fav, favN := "", 0
		for _, c := range []struct {
			id string
			n  int
		}{{"morning", tod.Morning}, {"noon", tod.Noon}, {"afternoon", tod.Afternoon}, {"evening", tod.Evening}} {
			if c.n > favN {
				fav, favN = c.id, c.n
			}
		}
		tod.Favorite = fav
		for h := range hours {
			if hours[h] > hours[tod.FavoriteHour] {
				tod.FavoriteHour = h
			}
		}
	}

	out.WeekStreak = longestWeekStreak(weeks)

	var topShoe primitive.ObjectID
	for id, m := range shoeM {
		if m > shoeM[topShoe] || (m == shoeM[topShoe] && id.Hex() < topShoe.Hex()) {
			topShoe = id
		}
	}
	if !topShoe.IsZero() {
		out.topShoeID = topShoe
		out.TopShoe = &wrappedShoeOut{
			ID:         topShoe.Hex(),
			DistanceKm: wrRound1(shoeM[topShoe] / 1000),
			Runs:       shoeRuns[topShoe],
		}
	}

	out.Profile = wrappedProfile(&out, longest)
	return out
}

// longestWeekStreak : plus longue suite de semaines (lundi → dimanche, heure
// de Paris) avec au moins une sortie.
func longestWeekStreak(weeks map[string]time.Time) wrappedStreakOut {
	if len(weeks) == 0 {
		return wrappedStreakOut{}
	}
	mondays := make([]time.Time, 0, len(weeks))
	for _, m := range weeks {
		mondays = append(mondays, m)
	}
	sort.Slice(mondays, func(i, j int) bool { return mondays[i].Before(mondays[j]) })

	best := wrappedStreakOut{}
	runStart, runLen := mondays[0], 1
	flush := func(last time.Time) {
		if runLen > best.Weeks {
			best = wrappedStreakOut{
				Weeks: runLen,
				From:  wrDayKey(runStart),
				To:    wrDayKey(last.AddDate(0, 0, 6)),
			}
		}
	}
	for i := 1; i < len(mondays); i++ {
		// AddDate plutôt que +168 h : les changements d'heure décalent d'une heure.
		if wrDayKey(mondays[i-1].AddDate(0, 0, 7)) == wrDayKey(mondays[i]) {
			runLen++
			continue
		}
		flush(mondays[i-1])
		runStart, runLen = mondays[i], 1
	}
	flush(mondays[len(mondays)-1])
	return best
}

func wrPct(n, total int) int {
	if total <= 0 {
		return 0
	}
	return int(math.Round(float64(n) * 100 / float64(total)))
}

// wrappedProfile : le « profil de coureur » de la période, un titre et une
// phrase. Les règles sont lues dans l'ordre : la première qui colle gagne, des
// traits les plus marqués aux plus communs.
func wrappedProfile(w *wrappedOut, longest *wrappedRun) *wrappedProfileOut {
	t := &w.Totals
	if t.Runs == 0 {
		return nil
	}
	if t.Runs < 4 {
		return &wrappedProfileOut{
			ID:      "rookie",
			Title:   "La jeune pousse",
			Tagline: "Quelques foulées pour commencer : le plus dur, se lancer, c'est fait.",
		}
	}
	st := &w.SessionTypes
	tod := &w.TimeOfDay
	if st.Total >= 3 && float64(st.Interval) >= 0.3*float64(st.Total) {
		return &wrappedProfileOut{
			ID:      "rocket",
			Title:   "La fusée",
			Tagline: fmt.Sprintf("%d %% de tes séances en fractionné : toi, tu cours pour aller vite.", wrPct(st.Interval, st.Total)),
		}
	}
	if float64(tod.EarlyRuns) >= 0.5*float64(t.Runs) {
		return &wrappedProfileOut{
			ID:      "early_bird",
			Title:   "Le lève-tôt",
			Tagline: fmt.Sprintf("%d %% de tes sorties avant 8 h : quand la ville se réveille, tu as déjà fini.", wrPct(tod.EarlyRuns, t.Runs)),
		}
	}
	if float64(tod.LateRuns) >= 0.5*float64(t.Runs) {
		return &wrappedProfileOut{
			ID:      "night_owl",
			Title:   "Le noctambule",
			Tagline: fmt.Sprintf("%d %% de tes sorties après 20 h : la ville dort, toi tu cours.", wrPct(tod.LateRuns, t.Runs)),
		}
	}
	elevPerKm := 0.0
	if t.DistanceKm > 0 {
		elevPerKm = t.ElevationGainM / t.DistanceKm
	}
	if elevPerKm >= 15 || t.StartPlaces >= 10 {
		tag := fmt.Sprintf("%d lieux de départ différents : tu ne cours jamais deux fois au même endroit.", t.StartPlaces)
		if elevPerKm >= 15 {
			tag = fmt.Sprintf("%.0f m de D+ : les côtes, tu les cherches au lieu de les éviter.", t.ElevationGainM)
		}
		return &wrappedProfileOut{ID: "explorer", Title: "L'explorateur", Tagline: tag}
	}
	if st.Total >= 3 && float64(st.Endurance) >= 0.5*float64(st.Total) {
		return &wrappedProfileOut{
			ID:      "metronome",
			Title:   "Le métronome",
			Tagline: fmt.Sprintf("%d %% de tes séances en endurance : régulier, patient, inarrêtable.", wrPct(st.Endurance, st.Total)),
		}
	}
	if t.AvgDistanceKm >= 12 || (longest != nil && longest.DistanceM >= 25000) {
		return &wrappedProfileOut{
			ID:      "long_hauler",
			Title:   "Le fondeur",
			Tagline: fmt.Sprintf("%.1f km en moyenne par sortie : les longues distances ne te font pas peur.", t.AvgDistanceKm),
		}
	}
	if t.ActiveWeeks > 0 && float64(t.Runs)/float64(t.ActiveWeeks) >= 3 {
		return &wrappedProfileOut{
			ID:      "relentless",
			Title:   "L'infatigable",
			Tagline: fmt.Sprintf("%.1f sorties par semaine active : la régularité, c'est ta signature.", float64(t.Runs)/float64(t.ActiveWeeks)),
		}
	}
	return &wrappedProfileOut{
		ID:      "free_spirit",
		Title:   "L'électron libre",
		Tagline: "Un peu de tout, au gré de tes envies : tu cours d'abord pour le plaisir.",
	}
}
