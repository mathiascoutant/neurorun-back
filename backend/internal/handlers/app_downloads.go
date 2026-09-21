package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"runapp/internal/appstore"
)

// Téléchargements App Store.
//
// Un rapport publié ne change plus : on le garde en mémoire sans expiration. Seuls les deux
// derniers jours sont réinterrogés — Apple les publie avec du retard, et un « pas encore de
// rapport » d’hier soir deviendrait faux ce matin.
const (
	appDownloadsMaxDays     = 60
	appDownloadsDefaultDays = 30
	// appDownloadsFreshDays : jours considérés comme encore mouvants.
	appDownloadsFreshDays = 2
	// appDownloadsFetchLimit : appels simultanés vers Apple. Trente jours en série prendraient
	// une dizaine de secondes ; trente d’un coup se feraient limiter.
	appDownloadsFetchLimit = 5
)

type appDownloadsCacheEntry struct {
	report  appstore.DayReport
	missing bool // rapport inexistant côté Apple (jour sans activité, ou pas encore publié)
	fetched time.Time
}

func (h *Handlers) appStoreClient() (*appstore.Client, error) {
	h.ascMu.Lock()
	defer h.ascMu.Unlock()
	if h.ascClient != nil {
		return h.ascClient, nil
	}
	c, err := appstore.New(
		h.cfg.AppleASCIssuerID,
		h.cfg.AppleASCKeyID,
		h.cfg.AppleASCKeyP8,
		h.cfg.AppleASCVendorNumber,
		h.cfg.AppleASCReportVersion,
	)
	if err != nil {
		return nil, err
	}
	h.ascClient = c
	return c, nil
}

func (h *Handlers) cachedDayReport(day string) (appDownloadsCacheEntry, bool) {
	h.ascMu.Lock()
	defer h.ascMu.Unlock()
	e, ok := h.ascCache[day]
	return e, ok
}

func (h *Handlers) storeDayReport(day string, e appDownloadsCacheEntry) {
	h.ascMu.Lock()
	defer h.ascMu.Unlock()
	if h.ascCache == nil {
		h.ascCache = map[string]appDownloadsCacheEntry{}
	}
	h.ascCache[day] = e
}

// AdminAppDownloads GET /api/admin/app-downloads?days=30
//
// Répond 200 même sans configuration : la console doit pouvoir expliquer ce qu’il manque, ce
// qu’une erreur HTTP ne permettrait pas de distinguer d’une panne.
func (h *Handlers) AdminAppDownloads(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.AppleASCConfigured() {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": false,
			"missing":    h.cfg.AppleASCMissing(),
		})
		return
	}

	days := appDownloadsDefaultDays
	if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			days = n
		}
	}
	if days > appDownloadsMaxDays {
		days = appDownloadsMaxDays
	}

	client, err := h.appStoreClient()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": true,
			"error":      err.Error(),
		})
		return
	}

	/* Le rapport de la veille n’est pas toujours publié : on part d’hier, pas d’aujourd’hui. */
	end := time.Now().UTC().AddDate(0, 0, -1)
	wanted := make([]string, 0, days)
	for i := days - 1; i >= 0; i-- {
		wanted = append(wanted, end.AddDate(0, 0, -i).Format("2006-01-02"))
	}

	reports, firstErr := h.fetchDayReports(r.Context(), client, wanted)

	sort.Slice(reports, func(i, j int) bool { return reports[i].Day < reports[j].Day })

	var totalInstalls, totalRedownloads, totalUpdates int64
	byCountry := map[string]int64{}
	byType := map[string]int64{}
	for _, rep := range reports {
		totalInstalls += rep.FirstInstalls
		totalRedownloads += rep.Redownloads
		totalUpdates += rep.Updates
		for cc, n := range rep.InstallsByCountry {
			byCountry[cc] += n
		}
		for t, n := range rep.UnitsByType {
			byType[t] += n
		}
	}

	body := map[string]any{
		"configured":          true,
		"days":                days,
		"daily":               reports,
		"first_installs":      totalInstalls,
		"redownloads":         totalRedownloads,
		"updates":             totalUpdates,
		"installs_by_country": byCountry,
		"units_by_type":       byType,
	}
	// Une erreur n’efface pas les jours déjà obtenus : mieux vaut un mois incomplet et signalé
	// qu’un écran vide.
	if firstErr != nil {
		body["error"] = firstErr.Error()
	}
	writeJSON(w, http.StatusOK, body)
}

// fetchDayReports va chercher les jours manquants, en parallèle borné, et renvoie la première
// erreur rencontrée sans jeter les jours obtenus.
func (h *Handlers) fetchDayReports(
	ctx context.Context,
	client *appstore.Client,
	wanted []string,
) ([]appstore.DayReport, error) {
	var (
		mu       sync.Mutex
		reports  []appstore.DayReport
		firstErr error
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, appDownloadsFetchLimit)
	fresh := time.Now().UTC().AddDate(0, 0, -appDownloadsFreshDays).Format("2006-01-02")

	for _, day := range wanted {
		if e, ok := h.cachedDayReport(day); ok && (day < fresh || time.Since(e.fetched) < time.Hour) {
			if !e.missing {
				mu.Lock()
				reports = append(reports, e.report)
				mu.Unlock()
			}
			continue
		}

		wg.Add(1)
		go func(day string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			rep, err := client.DailySummary(ctx, day)
			switch {
			case errors.Is(err, appstore.ErrNoReport):
				h.storeDayReport(day, appDownloadsCacheEntry{missing: true, fetched: time.Now()})
			case err != nil:
				log.Printf("app-downloads %s: %v", day, err)
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			default:
				h.storeDayReport(day, appDownloadsCacheEntry{report: rep, fetched: time.Now()})
				mu.Lock()
				reports = append(reports, rep)
				mu.Unlock()
			}
		}(day)
	}
	wg.Wait()
	return reports, firstErr
}
