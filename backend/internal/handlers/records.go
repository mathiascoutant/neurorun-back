package handlers

import (
	"context"
	"log"
	"math"
	"net/http"
	"sync"
	"time"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
	"runapp/internal/records"
	"runapp/internal/strava"
)

const (
	// Détails Strava lus par ouverture de l'écran : une requête chacun, sous le
	// quota de toute l'application. Le reste vient aux ouvertures suivantes, et
	// une sortie lue ne l'est plus jamais.
	recordsStravaFetchMax = 12
	// Sorties Strava examinées par distance et par période, les plus rapides
	// en allure moyenne d'abord.
	recordsStravaPerWindow = 3
	// Météo des records : quelques appels en parallèle, bornés dans le temps.
	recordsWeatherWorkers = 4
	recordsWeatherTimeout = 8 * time.Second
)

type recordsDistance struct {
	records.Distance
	Efforts []records.Effort `json:"efforts"`
}

// Records GET /api/records — records sur 5 km, 10 km, semi et marathon, depuis
// la toute première sortie, NeuroRun et Strava confondus. Chaque distance porte
// sa progression (les records successifs) et le record de chaque période que
// l'app propose ; ceux-là viennent avec la météo de l'heure.
func (h *Handlers) Records(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	ctx := r.Context()
	cutoffs := records.Cutoffs(time.Now().UTC())

	byDist := map[string][]records.Effort{}
	var firstRun time.Time
	saw := func(t time.Time) {
		if !t.IsZero() && (firstRun.IsZero() || t.Before(firstRun)) {
			firstRun = t
		}
	}

	live, err := h.db.ListLiveRunsForRecords(ctx, u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture des courses impossible"})
		return
	}
	// Courses NeuroRun publiées sur Strava : leur copie Strava n'est pas une
	// deuxième course.
	uploaded := map[int64]bool{}
	liveStarts := make([]strava.RunActivity, 0, len(live))
	for i := range live {
		lr := &live[i]
		start := goalcalendar.LiveRunStart(*lr)
		saw(start)
		if lr.Strava != nil && lr.Strava.ActivityID != 0 {
			uploaded[lr.Strava.ActivityID] = true
		}
		liveStarts = append(liveStarts, strava.RunActivity{StartAt: start, DistanceM: lr.DistanceM})

		var lat, lon *float64
		if len(lr.TrackPoints) > 0 {
			lat, lon = roundedCoord(lr.TrackPoints[0].Lat), roundedCoord(lr.TrackPoints[0].Lng)
		}
		for _, d := range records.Distances {
			t, offset, ok := records.FromSplits(lr.Splits, lr.DistanceM, lr.MovingSec, d.Meters)
			if !ok {
				continue
			}
			byDist[d.ID] = append(byDist[d.ID], records.Effort{
				TimeSec:      t,
				StartAt:      start,
				MidAt:        start.Add(time.Duration((offset + t/2) * float64(time.Second))),
				Source:       records.SourceNeuroRun,
				RunID:        lr.ID.Hex(),
				RunName:      lr.Name,
				RunDistanceM: lr.DistanceM,
				Lat:          lat,
				Lon:          lon,
			})
		}
	}

	partial := false
	if u.HasStrava() {
		partial = h.addStravaRecords(ctx, u, byDist, uploaded, liveStarts, cutoffs, saw)
	}

	out := make([]recordsDistance, 0, len(records.Distances))
	var withWeather []*records.Effort
	for _, d := range records.Distances {
		sel := records.Select(byDist[d.ID], cutoffs)
		for _, i := range records.WindowBests(sel, cutoffs) {
			if sel[i].Lat != nil && sel[i].Lon != nil {
				withWeather = append(withWeather, &sel[i])
			}
		}
		out = append(out, recordsDistance{Distance: d, Efforts: sel})
	}
	h.fillRecordsWeather(ctx, withWeather)

	resp := map[string]any{
		"distances":     out,
		"strava_linked": u.HasStrava(),
		"partial":       partial,
	}
	if !firstRun.IsZero() {
		resp["first_run_at"] = firstRun.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

// addStravaRecords ajoute les portions des sorties Strava, en lisant au
// passage le détail des plus prometteuses encore inconnues. Vrai quand il en
// reste à lire : l'app le dit, et la prochaine ouverture continue.
func (h *Handlers) addStravaRecords(
	ctx context.Context,
	u *models.User,
	byDist map[string][]records.Effort,
	uploaded map[int64]bool,
	liveStarts []strava.RunActivity,
	cutoffs []time.Time,
	saw func(time.Time),
) (partial bool) {
	access, err := h.ensureStravaAccess(ctx, u)
	var acts []strava.RunActivity
	if err == nil {
		acts, err = h.strava.FetchRunActivities(ctx, access, nil)
	}
	h.forgetStravaIfRevoked(ctx, u, err)
	if err != nil {
		log.Printf("records: sorties Strava: %v", err)
		return false
	}

	kept := make([]strava.RunActivity, 0, len(acts))
	for _, a := range acts {
		saw(a.StartAt)
		// Tapis : la distance vient d'un capteur de foulée, pas du GPS.
		if a.Trainer || uploaded[a.ID] || copiesNeuroRun(a, liveStarts) {
			continue
		}
		kept = append(kept, a)
	}

	known, err := h.db.StravaBestEffortsByUser(ctx, u.ID)
	if err != nil {
		log.Printf("records: portions Strava: %v", err)
		known = map[int64]models.StravaBestEfforts{}
	}
	knownIDs := make(map[int64]bool, len(known))
	for id := range known {
		knownIDs[id] = true
	}
	cands := make([]records.Candidate, 0, len(kept))
	for _, a := range kept {
		cands = append(cands, records.Candidate{ID: a.ID, StartAt: a.StartAt, DistanceM: a.DistanceM, MovingSec: float64(a.MovingSec)})
	}

	toFetch := records.StravaToFetch(cands, knownIDs, cutoffs, recordsStravaPerWindow)
	for n, id := range toFetch {
		if n >= recordsStravaFetchMax {
			partial = true
			break
		}
		det, err := h.strava.FetchActivityDetail(ctx, access, id)
		if err != nil {
			// Quota atteint ou Strava muet : on s'arrête là, la suite viendra.
			log.Printf("records: détail Strava %d: %v", id, err)
			partial = true
			break
		}
		efforts := stravaEffortsOf(det)
		if err := h.db.SaveStravaBestEfforts(ctx, u.ID, id, efforts); err != nil {
			log.Printf("records: enregistrement portions %d: %v", id, err)
		}
		known[id] = models.StravaBestEfforts{StravaID: id, Efforts: efforts}
	}

	for _, a := range kept {
		be, ok := known[a.ID]
		if !ok {
			continue
		}
		var lat, lon *float64
		if a.StartLat != nil && a.StartLng != nil {
			lat, lon = roundedCoord(*a.StartLat), roundedCoord(*a.StartLng)
		}
		for _, d := range records.Distances {
			e, ok := be.Efforts[d.ID]
			if !ok || !records.Plausible(e.TimeSec, d.Meters) {
				continue
			}
			from := e.StartAt
			if from.IsZero() {
				from = a.StartAt
			}
			byDist[d.ID] = append(byDist[d.ID], records.Effort{
				TimeSec:      e.TimeSec,
				StartAt:      a.StartAt,
				MidAt:        from.Add(time.Duration(e.TimeSec / 2 * float64(time.Second))),
				Source:       records.SourceStrava,
				StravaID:     a.ID,
				RunName:      a.Name,
				RunDistanceM: a.DistanceM,
				Lat:          lat,
				Lon:          lon,
			})
		}
	}
	return partial
}

// stravaEffortsOf : les portions d'une activité Strava sur nos distances.
func stravaEffortsOf(det *strava.DetailedActivity) map[string]models.StravaEffort {
	out := map[string]models.StravaEffort{}
	for _, be := range det.BestEfforts {
		for _, d := range records.Distances {
			if be.Name != d.StravaName || be.ElapsedTime <= 0 {
				continue
			}
			start, _ := time.Parse(time.RFC3339, be.StartDate)
			t := float64(be.ElapsedTime)
			if prev, ok := out[d.ID]; !ok || t < prev.TimeSec {
				out[d.ID] = models.StravaEffort{TimeSec: t, StartAt: start.UTC()}
			}
		}
	}
	return out
}

// copiesNeuroRun : sortie Strava qui reprend une course NeuroRun (même départ à
// quelques minutes près, même distance à 10 % près), publiée à la main ou par
// une montre reliée aux deux.
func copiesNeuroRun(a strava.RunActivity, liveStarts []strava.RunActivity) bool {
	if a.DistanceM <= 0 {
		return false
	}
	for _, l := range liveStarts {
		dt := a.StartAt.Sub(l.StartAt)
		if dt < 0 {
			dt = -dt
		}
		if dt <= 4*time.Minute && math.Abs(l.DistanceM-a.DistanceM)/a.DistanceM < 0.1 {
			return true
		}
	}
	return false
}

// fillRecordsWeather lit la météo de l'heure de chaque record, en parallèle et
// sans faire attendre l'écran au-delà d'un délai : un record sans météo reste
// un record.
func (h *Handlers) fillRecordsWeather(ctx context.Context, efforts []*records.Effort) {
	if len(efforts) == 0 {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, recordsWeatherTimeout)
	defer cancel()
	sem := make(chan struct{}, recordsWeatherWorkers)
	var wg sync.WaitGroup
	for _, e := range efforts {
		wg.Add(1)
		go func(e *records.Effort) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if c, err := h.weather.At(wctx, *e.Lat, *e.Lon, e.MidAt); err == nil {
				e.Weather = c
			}
		}(e)
	}
	wg.Wait()
}

// roundedCoord : départ arrondi au millième (une centaine de mètres) — assez
// pour nommer la ville et lire la météo, sans redonner le pas de la porte.
func roundedCoord(x float64) *float64 {
	v := math.Round(x*1000) / 1000
	return &v
}
