package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"runapp/internal/models"
	"runapp/internal/store"
	"runapp/internal/vma"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const maxVmaTestTrackPoints = 3000

type vmaTestCreateBody struct {
	ClientTestID string                     `json:"client_test_id"`
	DistanceM    float64                    `json:"distance_m"`
	DurationSec  float64                    `json:"duration_sec"`
	Source       string                     `json:"source"`
	TrackPoints  []models.LiveRunTrackPoint `json:"track_points"`
}

func fmtPace(secPerKm float64) string {
	if secPerKm <= 0 {
		return "—"
	}
	s := int(math.Round(secPerKm))
	return fmt.Sprintf("%d:%02d/km", s/60, s%60)
}

func fmtDuration(sec float64) string {
	if sec <= 0 {
		return "—"
	}
	s := int(math.Round(sec))
	if s >= 3600 {
		return fmt.Sprintf("%dh%02d:%02d", s/3600, (s%3600)/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// signedSec : « +12 s » / « -8 s », pour lire un écart d'un coup d'œil.
func signedSec(sec float64) string {
	s := int(math.Round(sec))
	if s >= 0 {
		return fmt.Sprintf("+%d s", s)
	}
	return fmt.Sprintf("%d s", s)
}

// CreateVmaTest enregistre un test de VMA de 6 minutes. Repassable à volonté :
// c'est le test le plus récent qui fait référence.
func (h *Handlers) CreateVmaTest(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "live_runs") {
		return
	}
	var b vmaTestCreateBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}

	if b.DistanceM <= 0 || b.DistanceM > 5000 || math.IsNaN(b.DistanceM) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "distance_m invalide"})
		return
	}
	// On tolère un arrêt un peu tardif, pas un test tronqué : sous 5 min la
	// mesure ne veut plus rien dire.
	if b.DurationSec < 300 || b.DurationSec > 420 || math.IsNaN(b.DurationSec) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "duration_sec invalide"})
		return
	}

	vmaKmh := vma.FromTest(b.DistanceM, b.DurationSec)
	if !vma.PlausibleVma(vmaKmh) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "vma hors bornes plausibles — test à refaire",
		})
		return
	}

	clientTestID := strings.TrimSpace(b.ClientTestID)
	if len(clientTestID) > maxClientRunIDLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "client_test_id invalide"})
		return
	}
	if clientTestID != "" {
		if existing, err := h.db.FindVmaTestByClientID(r.Context(), u.ID, clientTestID); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"id": existing.ID.Hex(), "vma_kmh": existing.VmaKmh, "duplicate": true,
			})
			return
		}
	}

	if len(b.TrackPoints) > maxVmaTestTrackPoints {
		b.TrackPoints = b.TrackPoints[:maxVmaTestTrackPoints]
	}
	source := strings.TrimSpace(strings.ToLower(b.Source))
	if source != "watch" {
		source = "iphone"
	}

	t := models.VmaTest{
		UserID:       u.ID,
		ClientTestID: clientTestID,
		DistanceM:    b.DistanceM,
		DurationSec:  b.DurationSec,
		VmaKmh:       vmaKmh,
		Source:       source,
		TrackPoints:  b.TrackPoints,
	}
	if err := h.db.CreateVmaTest(r.Context(), &t); err != nil {
		if errors.Is(err, store.ErrDuplicateVmaTest) && clientTestID != "" {
			if existing, ferr := h.db.FindVmaTestByClientID(r.Context(), u.ID, clientTestID); ferr == nil {
				writeJSON(w, http.StatusOK, map[string]any{
					"id": existing.ID.Hex(), "vma_kmh": existing.VmaKmh, "duplicate": true,
				})
				return
			}
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         t.ID.Hex(),
		"created_at": t.CreatedAt.Format(time.RFC3339),
		"vma_kmh":    math.Round(t.VmaKmh*10) / 10,
		"targets":    vmaTargets(t.VmaKmh),
	})
}

// vmaTargets : tableau des allures visées par distance, pour l'onglet Prévision.
func vmaTargets(vmaKmh float64) []map[string]any {
	out := make([]map[string]any, 0, len(vma.StandardDistances))
	for _, d := range vma.StandardDistances {
		tg := vma.TargetFor(vmaKmh, d.Km)
		out = append(out, map[string]any{
			"id":              d.ID,
			"distance_km":     d.Km,
			"pace_sec_per_km": tg.PaceSecPerKm,
			"time_sec":        tg.TimeSec,
			"fraction_of_vma": math.Round(tg.FractionOfVma*1000) / 1000,
		})
	}
	return out
}

// GetVma renvoie la VMA courante, les allures qui en découlent et l'historique
// des tests.
func (h *Handlers) GetVma(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "live_runs") {
		return
	}

	tests, err := h.db.ListVmaTestsByUser(r.Context(), u.ID, 20)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	history := make([]models.VmaTestListItem, 0, len(tests))
	for _, t := range tests {
		history = append(history, models.VmaTestListItem{
			ID:          t.ID.Hex(),
			CreatedAt:   t.CreatedAt.UTC().Format(time.RFC3339),
			DistanceM:   t.DistanceM,
			DurationSec: t.DurationSec,
			VmaKmh:      math.Round(t.VmaKmh*10) / 10,
			Source:      t.Source,
		})
	}

	resp := map[string]any{
		"has_test":          len(tests) > 0,
		"history":           history,
		"test_duration_sec": vma.TestDurationSec,
	}
	if len(tests) > 0 {
		cur := tests[0]
		resp["vma_kmh"] = math.Round(cur.VmaKmh*10) / 10
		resp["tested_at"] = cur.CreatedAt.UTC().Format(time.RFC3339)
		resp["test_id"] = cur.ID.Hex()
		resp["targets"] = vmaTargets(cur.VmaKmh)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handlers) DeleteVmaTest(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "live_runs") {
		return
	}
	id, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	if err := h.db.DeleteVmaTest(r.Context(), u.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "test introuvable"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// scoreLiveRun calcule la note d'une course avec la VMA en vigueur au moment
// de l'enregistrement. Sans test de VMA, pas de note : on renvoie nil et
// l'app invite à passer le test.
func (h *Handlers) scoreLiveRun(ctx context.Context, userID primitive.ObjectID, run *models.LiveRun) *models.RunScore {
	test, err := h.db.LatestVmaTest(ctx, userID)
	if err != nil || test == nil || !vma.PlausibleVma(test.VmaKmh) {
		return nil
	}

	splits := make([]vma.Split, 0, len(run.Splits))
	for _, s := range run.Splits {
		splits = append(splits, vma.Split{Km: s.Km, PaceSecPerKm: s.PaceSecPerKm})
	}
	// Le temps de mouvement, pas le temps écoulé : les pauses ne doivent pas
	// être comptées comme de la course lente.
	res := vma.Score(splits, run.DistanceM/1000, run.MovingSec, test.VmaKmh)
	if !res.Scorable {
		return nil
	}
	return &models.RunScore{
		Result:     res,
		VmaTestID:  test.ID.Hex(),
		ComputedAt: time.Now().UTC(),
	}
}

// GetLiveRunScore renvoie la note d'une course, et rédige son explication à la
// première consultation (puis la mémorise).
func (h *Handlers) GetLiveRunScore(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "live_runs") {
		return
	}
	runID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	run, err := h.db.GetLiveRunByUser(r.Context(), u.ID, runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "course introuvable"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}

	// Courses enregistrées avant la fonctionnalité, ou avant le premier test de
	// VMA : on note maintenant, et on rattrape en base.
	score := run.Score
	backfilled := false
	if score == nil {
		score = h.scoreLiveRun(r.Context(), u.ID, run)
		backfilled = score != nil
	}
	if score == nil {
		hasTest := h.userHasVmaTest(r.Context(), u.ID)
		reason := "vma_absente"
		if hasTest {
			// VMA connue mais course inexploitable : trop courte, ou sans
			// kilomètre complet.
			reason = "course_non_notable"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"scored":         false,
			"needs_vma_test": !hasTest,
			"reason":         reason,
		})
		return
	}

	if score.Explanation == nil {
		score.Explanation = h.explainScore(r.Context(), score)
	}
	// Meilleur effort : si l'écriture échoue, tout sera recalculé à la prochaine
	// ouverture. On écrit la note entière quand elle manquait — n'écrire que
	// `score.explanation` créerait une note vide.
	if backfilled {
		_ = h.db.SetLiveRunScore(r.Context(), u.ID, runID, score)
	} else {
		_ = h.db.SetLiveRunScoreExplanation(r.Context(), u.ID, runID, score.Explanation)
	}

	writeJSON(w, http.StatusOK, map[string]any{"scored": true, "score": score})
}

type scoreExplanationJSON struct {
	Summary string   `json:"summary"`
	Why     []string `json:"why"`
	Advice  []string `json:"advice"`
}

// explainScore rédige le « pourquoi » de la note. La note elle-même reste
// déterministe : l'IA ne fait que la mettre en mots, et un secours prend le
// relais si elle est indisponible.
func (h *Handlers) explainScore(ctx context.Context, s *models.RunScore) *models.RunScoreExplanation {
	if strings.TrimSpace(h.cfg.OpenAIAPIKey) != "" {
		if exp, err := h.explainScoreAI(ctx, s); err == nil {
			return exp
		}
	}
	return fallbackExplanation(s)
}

func (h *Handlers) explainScoreAI(ctx context.Context, s *models.RunScore) (*models.RunScoreExplanation, error) {
	var kms strings.Builder
	for _, k := range s.Kms {
		fmt.Fprintf(&kms, "km %d : %s (%s) ; ", k.Km, fmtPace(k.PaceSecPerKm), signedSec(k.DeltaSec))
	}

	prompt := fmt.Sprintf(`Tu es un coach course à pied. Explique une note de course à son auteur, en français, en le tutoyant.

Note obtenue : %d/100 (régularité %d/100, chrono %d/100).
VMA de référence : %.1f km/h.
Distance : %.2f km. Objectif d'allure : %s, soit un temps visé de %s.
Temps réalisé : %s (%s par rapport à l'objectif).
Kilomètres : %s
Écart moyen à l'allure visée : %.1f s/km. Amplitude entre le km le plus rapide et le plus lent : %.0f s.
Dérive seconde moitié - première moitié : %s. Kilomètres dans la fourchette (±2 s) : %d sur %d.

La note récompense la régularité (70 %%) et le respect du chrono visé (30 %%). S'écarter de l'allure visée, plus vite comme plus lent, fait baisser la note.

Réponds UNIQUEMENT par un objet JSON valide, sans markdown :
{"summary": string, "why": [string, string, string], "advice": [string, string, string]}

- summary : une phrase, ce qui résume la course.
- why : 2 à 3 raisons concrètes de cette note, chiffrées, en citant les kilomètres concernés.
- advice : 2 à 3 conseils actionnables pour progresser sur ce point précis.
Pas de généralités creuses, appuie-toi sur les chiffres fournis.`,
		s.Total, s.Regularity, s.Chrono,
		s.VmaKmh, s.DistanceKm, fmtPace(s.TargetPaceSecPerKm), fmtDuration(s.TargetTimeSec),
		fmtDuration(s.ActualTimeSec), signedSec(s.ChronoDeltaSec),
		strings.TrimSuffix(kms.String(), " ; "),
		s.MeanAbsDevSec, s.SpreadSec, signedSec(s.DriftSec), s.KmsInBand, len(s.Kms))

	raw, err := h.openai.Chat(ctx, "Tu réponds uniquement en JSON minimal, sans balises.", prompt)
	if err != nil {
		return nil, err
	}
	txt := strings.TrimSpace(raw)
	if i := strings.Index(txt, "{"); i >= 0 {
		txt = txt[i:]
	}
	if j := strings.LastIndex(txt, "}"); j >= 0 {
		txt = txt[:j+1]
	}
	var parsed scoreExplanationJSON
	if err := json.Unmarshal([]byte(txt), &parsed); err != nil {
		return nil, err
	}
	if strings.TrimSpace(parsed.Summary) == "" || len(parsed.Why) == 0 {
		return nil, errors.New("explication vide")
	}
	return &models.RunScoreExplanation{
		Summary:   strings.TrimSpace(parsed.Summary),
		Why:       parsed.Why,
		Advice:    parsed.Advice,
		AiUsed:    true,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// fallbackExplanation : explication chiffrée sans IA. Moins bien tournée, mais
// jamais absente — et elle dit exactement la même chose que la note.
func fallbackExplanation(s *models.RunScore) *models.RunScoreExplanation {
	var summary string
	switch s.Band {
	case "exceptionnel":
		summary = "Course tenue au cordeau : tu as collé à l'allure visée du début à la fin."
	case "tres_bon":
		summary = "Très bonne exécution, à quelques secondes près de l'allure visée."
	case "bon":
		summary = "Bonne sortie, régulière dans l'ensemble, avec encore de la marge sur la constance."
	case "correct":
		summary = "Sortie correcte, mais l'allure a trop varié d'un kilomètre à l'autre."
	default:
		summary = "Course loin du plan : l'allure visée n'a pas été tenue."
	}

	why := []string{
		fmt.Sprintf("Objectif %s sur %.2f km, soit %s visées ; tu as mis %s (%s).",
			fmtPace(s.TargetPaceSecPerKm), s.DistanceKm, fmtDuration(s.TargetTimeSec),
			fmtDuration(s.ActualTimeSec), signedSec(s.ChronoDeltaSec)),
		fmt.Sprintf("Écart moyen de %.1f s/km à l'allure visée, %d km sur %d dans la fourchette.",
			s.MeanAbsDevSec, s.KmsInBand, len(s.Kms)),
	}
	if s.SpreadSec > 0 {
		why = append(why, fmt.Sprintf("%.0f s d'amplitude entre ton km le plus rapide (km %d) et le plus lent (km %d).",
			s.SpreadSec, s.BestKm, s.WorstKm))
	}

	advice := []string{}
	if s.DriftSec > 10 {
		advice = append(advice, fmt.Sprintf("Tu perds %s au kilomètre sur la seconde moitié : pars plus prudemment, quitte à te sentir trop lent sur les deux premiers kilomètres.", signedSec(s.DriftSec)))
	} else if s.DriftSec < -10 {
		advice = append(advice, "Tu finis nettement plus vite que tu ne commences : tu as de la marge pour engager plus tôt.")
	}
	if s.SpreadSec > 20 {
		advice = append(advice, "Cale-toi sur l'allure affichée toutes les 30 s plutôt que sur la sensation : c'est l'amplitude entre tes kilomètres qui coûte le plus de points.")
	}
	if s.StaleVmaHint {
		advice = append(advice, "Tu vas nettement plus vite que ce que prévoit ta VMA : refais le test de 6 minutes, ta référence a vieilli.")
	}
	if len(advice) == 0 {
		advice = append(advice, "Continue sur ce rythme : vise à réduire encore l'écart kilomètre par kilomètre.")
	}

	return &models.RunScoreExplanation{
		Summary:   summary,
		Why:       why,
		Advice:    advice,
		AiUsed:    false,
		CreatedAt: time.Now().UTC(),
	}
}
