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

// runVma : la VMA qui juge une course. Une course déjà notée garde la VMA de
// sa note — elle juge l'exécution du jour, avec la référence du jour — même
// quand on la recalcule ; sinon, le dernier test.
func (h *Handlers) runVma(ctx context.Context, userID primitive.ObjectID, run *models.LiveRun) (float64, string, bool) {
	if run.Score != nil && vma.PlausibleVma(run.Score.VmaKmh) {
		return run.Score.VmaKmh, run.Score.VmaTestID, true
	}
	test, err := h.db.LatestVmaTest(ctx, userID)
	if err != nil || test == nil || !vma.PlausibleVma(test.VmaKmh) {
		return 0, "", false
	}
	return test.VmaKmh, test.ID.Hex(), true
}

// scoreLiveRun calcule la note d'une course. sessionType force le type de
// séance ("" = déduit de l'allure). Sans test de VMA, pas de note : on renvoie
// nil et l'app invite à passer le test.
func (h *Handlers) scoreLiveRun(ctx context.Context, u *models.User, run *models.LiveRun, sessionType string) *models.RunScore {
	vmaKmh, testID, ok := h.runVma(ctx, u.ID, run)
	if !ok {
		return nil
	}
	res := vma.Score(liveRunScoreInput(run, vmaKmh, u.BirthDate, sessionType))
	if !res.Scorable {
		return nil
	}
	return &models.RunScore{
		Result:     res,
		VmaTestID:  testID,
		ComputedAt: time.Now().UTC(),
	}
}

// liveRunScoreInput : ce que la note lit d'une course enregistrée.
func liveRunScoreInput(run *models.LiveRun, vmaKmh float64, birthDate, sessionType string) vma.Input {
	splits := make([]vma.Split, 0, len(run.Splits))
	for _, s := range run.Splits {
		splits = append(splits, vma.Split{Km: s.Km, PaceSecPerKm: s.PaceSecPerKm, EndMs: s.EndTimestampMs})
	}

	// Départ : l'horodatage GPS, sinon le premier point de la trace.
	start := run.GpsStartTsMs
	hr := make([]vma.HrSample, 0, len(run.TrackPoints))
	for _, p := range run.TrackPoints {
		if run.GpsStartTsMs <= 0 && p.TMs > 0 && (start <= 0 || p.TMs < start) {
			start = p.TMs
		}
		if p.HrBpm != nil && *p.HrBpm > 0 {
			hr = append(hr, vma.HrSample{TMs: p.TMs, Bpm: *p.HrBpm})
		}
	}

	// L'âge du jour de la course, pas celui du jour où on la relit.
	at := run.CreatedAt
	if start > 0 {
		at = time.UnixMilli(start)
	}
	if at.IsZero() {
		at = time.Now()
	}

	return vma.Input{
		Splits:     splits,
		DistanceKm: run.DistanceM / 1000,
		// Le temps de mouvement, pas le temps écoulé : les pauses ne doivent
		// pas être comptées comme de la course lente.
		MovingSec:   run.MovingSec,
		VmaKmh:      vmaKmh,
		StartMs:     start,
		Hr:          hr,
		HrMaxBpm:    vma.EstimateHrMax(birthDate, at),
		SessionType: sessionType,
	}
}

// keptSessionType : le type de séance choisi par l'utilisateur, à conserver
// quand la note est recalculée ("" = détection automatique).
func keptSessionType(sc *models.RunScore) string {
	if sc == nil || sc.SessionAuto {
		return ""
	}
	return sc.SessionType
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

	// Courses enregistrées avant la fonctionnalité, avant le premier test de
	// VMA, ou notées avec un barème antérieur : on note maintenant, et on
	// rattrape en base.
	score := run.Score
	rescored := false
	if score == nil || score.Version < vma.ScoreVersion {
		if fresh := h.scoreLiveRun(r.Context(), u, run, keptSessionType(score)); fresh != nil {
			score, rescored = fresh, true
		}
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

	h.respondScore(w, r, u.ID, runID, score, rescored)
}

type sessionTypeBody struct {
	// SessionType : "endurance", "tempo", "race", ou "auto" pour revenir à la
	// détection par l'allure.
	SessionType string `json:"session_type"`
}

// SetLiveRunSessionType PUT /api/live-runs/{id}/score/session — corrige le
// type de séance. La détection ne connaît que l'allure : un 10 km à fond raté
// passe pour une allure soutenue, un footing trop rapide aussi. Le coureur
// rétablit ce qu'il voulait faire, la note est recalculée en conséquence.
func (h *Handlers) SetLiveRunSessionType(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "live_runs") {
		return
	}
	runID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	var b sessionTypeBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corps invalide"})
		return
	}
	sessionType := strings.TrimSpace(b.SessionType)
	if sessionType == "auto" {
		sessionType = ""
	} else if !vma.ValidSessionType(sessionType) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type de séance inconnu"})
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
	score := h.scoreLiveRun(r.Context(), u, run, sessionType)
	if score == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "course non notable"})
		return
	}
	h.respondScore(w, r, u.ID, runID, score, true)
}

// respondScore rédige l'explication si elle manque, mémorise ce qui a changé
// et renvoie la note.
func (h *Handlers) respondScore(
	w http.ResponseWriter, r *http.Request, userID, runID primitive.ObjectID,
	score *models.RunScore, rescored bool,
) {
	explained := false
	if score.Explanation == nil {
		score.Explanation = h.explainScore(r.Context(), score)
		explained = true
	}
	// Meilleur effort : si l'écriture échoue, tout sera recalculé à la prochaine
	// ouverture. On écrit la note entière quand elle a été recalculée —
	// n'écrire que `score.explanation` créerait une note vide.
	if rescored {
		_ = h.db.SetLiveRunScore(r.Context(), userID, runID, score)
	} else if explained {
		_ = h.db.SetLiveRunScoreExplanation(r.Context(), userID, runID, score.Explanation)
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

// sessionLabel : le type de séance, tel qu'on le nomme au coureur.
func sessionLabel(t string) string {
	switch t {
	case vma.SessionEndurance:
		return "endurance fondamentale"
	case vma.SessionTempo:
		return "allure soutenue"
	default:
		return "course à fond"
	}
}

// componentScore : points d'une composante, et si elle a été mesurée.
func componentScore(s *models.RunScore, key string) (int, bool) {
	for _, c := range s.Components {
		if c.Key == key {
			return c.Score, true
		}
	}
	return 0, false
}

func (h *Handlers) explainScoreAI(ctx context.Context, s *models.RunScore) (*models.RunScoreExplanation, error) {
	var kms strings.Builder
	for _, k := range s.Kms {
		fmt.Fprintf(&kms, "km %d : %s (%s)", k.Km, fmtPace(k.PaceSecPerKm), signedSec(k.DeltaSec))
		if k.HrBpm > 0 {
			fmt.Fprintf(&kms, ", %.0f bpm", k.HrBpm)
		}
		kms.WriteString(" ; ")
	}
	var comps []string
	for _, c := range s.Components {
		comps = append(comps, fmt.Sprintf("%s %d/100 (%d %%)", strings.ToLower(c.Label), c.Score, c.Weight))
	}
	origin := "déduit de l'allure moyenne"
	if !s.SessionAuto {
		origin = "choisi par le coureur"
	}

	var detail, rules string
	if s.SessionType == vma.SessionRace {
		detail = fmt.Sprintf(`Objectif d'allure : %s, soit un temps visé de %s.
Temps réalisé : %s (%s par rapport à l'objectif).
Kilomètres (écart à l'allure visée) : %s
Écart moyen à l'allure visée : %.1f s/km. Kilomètres dans la fourchette (±2 s) : %d sur %d.`,
			fmtPace(s.TargetPaceSecPerKm), fmtDuration(s.TargetTimeSec),
			fmtDuration(s.ActualTimeSec), signedSec(s.ChronoDeltaSec),
			strings.TrimSuffix(kms.String(), " ; "), s.MeanAbsDevSec, s.KmsInBand, len(s.Kms))
		rules = "La note compare chaque kilomètre à l'allure que la VMA rend tenable sur la distance : s'en écarter, plus vite comme plus lent, fait baisser la note."
	} else {
		detail = fmt.Sprintf(`Temps : %s, allure moyenne %s, soit %.0f %% de la VMA.
Zone d'allure de la séance : %s à %s. Kilomètres dans la zone : %d sur %d.
Kilomètres (écart à la zone, 0 = dans la zone) : %s
Écart moyen à l'allure moyenne de la sortie : %.1f s/km.`,
			fmtDuration(s.ActualTimeSec), fmtPace(s.ActualTimeSec/s.DistanceKm), s.IntensityPct,
			fmtPace(s.ZoneFastPaceSecPerKm), fmtPace(s.ZoneSlowPaceSecPerKm), s.KmsInBand, len(s.Kms),
			strings.TrimSuffix(kms.String(), " ; "), s.MeanAbsDevSec)
		if s.HrCeilingBpm > 0 && s.HrInZonePct != nil {
			detail += fmt.Sprintf("\nFC moyenne %.0f bpm. Plafond de la zone : %.0f bpm (FC max estimée %.0f bpm). Temps passé sous le plafond : %.0f %%.",
				s.HrAvgBpm, s.HrCeilingBpm, s.HrMaxBpm, *s.HrInZonePct)
		} else if s.HrAvgBpm > 0 {
			detail += fmt.Sprintf("\nFC moyenne %.0f bpm (FC max inconnue : pas de zone cardio).", s.HrAvgBpm)
		} else {
			detail += "\nPas de fréquence cardiaque mesurée : la note ne juge que l'allure."
		}
		if s.DecouplingPct != nil {
			detail += fmt.Sprintf("\nDérive cardiaque (perte d'efficacité allure/FC entre les deux moitiés) : %.1f %%.", *s.DecouplingPct)
		}
		rules = "La note ne cherche pas la vitesse : elle vérifie que la séance a été courue comme elle devait l'être — cœur sous le plafond de la zone, kilomètres dans la zone d'allure, allure régulière, cœur qui ne dérive pas sur la fin. Courir plus vite que la zone fait baisser la note."
	}

	prompt := fmt.Sprintf(`Tu es un coach course à pied. Explique une note de course à son auteur, en français, en le tutoyant.

Type de séance : %s (%s).
Note obtenue : %d/100. Composantes : %s.
VMA de référence : %.1f km/h. Distance : %.2f km.
%s
Amplitude entre le km le plus rapide et le plus lent : %.0f s. Dérive d'allure seconde moitié - première moitié : %s.

%s

Réponds UNIQUEMENT par un objet JSON valide, sans markdown :
{"summary": string, "why": [string, string, string], "advice": [string, string, string]}

- summary : une phrase, ce qui résume la course.
- why : 2 à 3 raisons concrètes de cette note, chiffrées, en citant les kilomètres concernés.
- advice : 2 à 3 conseils actionnables pour progresser sur ce type de séance.
Pas de généralités creuses, appuie-toi sur les chiffres fournis.`,
		sessionLabel(s.SessionType), origin,
		s.Total, strings.Join(comps, ", "),
		s.VmaKmh, s.DistanceKm,
		detail,
		s.SpreadSec, signedSec(s.DriftSec),
		rules)

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
	var exp *models.RunScoreExplanation
	if s.SessionType == vma.SessionEndurance || s.SessionType == vma.SessionTempo {
		exp = fallbackSteady(s)
	} else {
		exp = fallbackRace(s)
	}
	exp.CreatedAt = time.Now().UTC()
	return exp
}

func fallbackRace(s *models.RunScore) *models.RunScoreExplanation {
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
			s.SpreadSec, s.FastestKm, s.SlowestKm))
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

	return &models.RunScoreExplanation{Summary: summary, Why: why, Advice: advice}
}

func fallbackSteady(s *models.RunScore) *models.RunScoreExplanation {
	label := sessionLabel(s.SessionType)
	var summary string
	switch s.Band {
	case "exceptionnel":
		summary = fmt.Sprintf("Séance d'%s menée parfaitement : le bon effort, tenu du début à la fin.", label)
	case "tres_bon":
		summary = fmt.Sprintf("Très bonne séance d'%s, à l'effort juste presque tout du long.", label)
	case "bon":
		summary = fmt.Sprintf("Bonne séance d'%s, avec encore de la marge sur la constance de l'effort.", label)
	case "correct":
		summary = fmt.Sprintf("Séance d'%s correcte, mais l'effort est sorti de sa zone.", label)
	default:
		summary = fmt.Sprintf("Séance loin d'une %s : l'effort n'est pas resté dans sa zone.", label)
	}

	why := []string{
		fmt.Sprintf("Allure moyenne %s, soit %.0f %% de ta VMA ; zone de la séance %s à %s, %d km sur %d dedans.",
			fmtPace(s.ActualTimeSec/s.DistanceKm), s.IntensityPct,
			fmtPace(s.ZoneFastPaceSecPerKm), fmtPace(s.ZoneSlowPaceSecPerKm), s.KmsInBand, len(s.Kms)),
	}
	if s.HrCeilingBpm > 0 && s.HrInZonePct != nil {
		why = append(why, fmt.Sprintf("FC moyenne %.0f bpm, %.0f %% du temps sous %.0f bpm, le plafond de la zone.",
			s.HrAvgBpm, *s.HrInZonePct, s.HrCeilingBpm))
	}
	if s.DecouplingPct != nil {
		why = append(why, fmt.Sprintf("Dérive cardiaque de %.1f %% entre la première et la seconde moitié.", *s.DecouplingPct))
	}
	why = append(why, fmt.Sprintf("Écart moyen de %.1f s/km à ton allure moyenne, %.0f s entre ton km le plus rapide (km %d) et le plus lent (km %d).",
		s.MeanAbsDevSec, s.SpreadSec, s.FastestKm, s.SlowestKm))

	advice := []string{}
	if pts, ok := componentScore(s, "hr_zone"); ok && pts < 80 && s.HrInZonePct != nil {
		advice = append(advice, fmt.Sprintf("Ton cœur a passé %.0f %% du temps au-dessus de %.0f bpm : ralentis dès que tu dépasses, quitte à marcher dans les côtes.",
			100-*s.HrInZonePct, s.HrCeilingBpm))
	}
	tooFast := 0
	for _, k := range s.Kms {
		if k.DeltaSec < 0 {
			tooFast++
		}
	}
	if tooFast > 0 {
		advice = append(advice, fmt.Sprintf("%d km plus rapides que %s : en %s, aller trop vite est la faute la plus courante, reste dans la zone.",
			tooFast, fmtPace(s.ZoneFastPaceSecPerKm), label))
	}
	if s.DecouplingPct != nil && *s.DecouplingPct > 5 {
		advice = append(advice, fmt.Sprintf("Ton cœur a dérivé de %.1f %% sur la seconde moitié : l'allure était un peu haute pour la durée, ou l'hydratation est à revoir.", *s.DecouplingPct))
	}
	if pts, ok := componentScore(s, "regularity"); ok && pts < 75 {
		advice = append(advice, "Cale-toi sur ton cardio ou l'allure affichée plutôt que sur la sensation : les écarts entre tes kilomètres coûtent des points.")
	}
	if s.HrAvgBpm == 0 && s.SessionType == vma.SessionEndurance {
		advice = append(advice, "Cours avec ta montre pour que la note tienne compte de ton cardio : en endurance, c'est lui qui dit si l'allure était la bonne.")
	}
	if len(advice) == 0 {
		advice = append(advice, "Continue comme ça : c'est ce type de séance, répétée, qui construit ton endurance.")
	}

	return &models.RunScoreExplanation{Summary: summary, Why: why, Advice: advice}
}
