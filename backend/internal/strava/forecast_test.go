package strava

import (
	"math"
	"testing"
	"time"
)

var refNow = time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

// mkRun crée une sortie à `daysAgo` jours, de `km` kilomètres, tenue à `paceMinKm`.
func mkRun(daysAgo, km, paceMinKm float64) RunActivity {
	speed := 1000 / (paceMinKm * 60) // m/s
	return RunActivity{
		ID:        1,
		Type:      "Run",
		StartAt:   refNow.Add(-time.Duration(daysAgo*24) * time.Hour),
		DistanceM: km * 1000,
		MovingSec: int(math.Round(km * paceMinKm * 60)),
		AvgSpeed:  speed,
	}
}

func legByID(p RaceForecastPayload, id string) RaceLegForecast {
	for _, l := range p.Legs {
		if l.ID == id {
			return l
		}
	}
	return RaceLegForecast{}
}

func TestWeightedPercentileUniformMatchesPlainPercentile(t *testing.T) {
	vals := []float64{4, 5, 6, 7, 8}
	items := make([]weighted, len(vals))
	for i, v := range vals {
		items[i] = weighted{v: v, w: 1}
	}
	// Quantile pondéré au centre des segments : la médiane doit tomber sur la valeur centrale.
	if got := weightedPercentile(items, 0.5); math.Abs(got-6) > 1e-9 {
		t.Fatalf("médiane pondérée = %v, attendu 6", got)
	}
	if got := weightedPercentile(items, 0); got != 4 {
		t.Fatalf("p0 = %v, attendu 4", got)
	}
	if got := weightedPercentile(items, 1); got != 8 {
		t.Fatalf("p100 = %v, attendu 8", got)
	}
}

func TestWeightedPercentileHonoursWeights(t *testing.T) {
	// Une valeur lente qui pèse 100× doit tirer le quantile vers elle.
	items := []weighted{{v: 4, w: 1}, {v: 6, w: 100}}
	got := weightedPercentile(items, 0.15)
	if got < 5.5 {
		t.Fatalf("p15 = %v : le poids de la valeur 6 n'est pas pris en compte", got)
	}
}

func TestRiegelPaceRoundTrip(t *testing.T) {
	// Monter en distance ralentit l'allure, descendre l'accélère.
	up := riegelPace(5.0, 10, raceMarathonKm)
	if up <= 5.0 {
		t.Fatalf("allure marathon %v devrait être plus lente que 5:00/km", up)
	}
	down := riegelPace(5.0, raceMarathonKm, race5kKm)
	if down >= 5.0 {
		t.Fatalf("allure 5 km %v devrait être plus rapide que 5:00/km", down)
	}
	// Identité.
	if got := riegelPace(5.0, 10, 10); math.Abs(got-5.0) > 1e-9 {
		t.Fatalf("riegelPace identité = %v", got)
	}
}

func TestRecencyBeatsAncientPeak(t *testing.T) {
	// Un pic de forme il y a 15 mois ne doit pas dominer 10 sorties récentes plus lentes.
	runs := []RunActivity{mkRun(450, 10, 4.0)}
	for i := 0; i < 10; i++ {
		runs = append(runs, mkRun(float64(3+i*3), 10, 5.0))
	}
	leg := legByID(buildRaceForecastAt(runs, refNow), "10k")
	if leg.TimeSec <= 0 {
		t.Fatal("pas de prévision 10k")
	}
	pace := leg.PaceSecPerKm / 60
	if pace < 4.6 {
		t.Fatalf("allure 10k = %.2f min/km : la vieille sortie pèse trop", pace)
	}
	if pace > 5.05 {
		t.Fatalf("allure 10k = %.2f min/km : le percentile bon jour ne joue plus", pace)
	}
}

func TestRunsOutsideLegacyBucketsAreUsed(t *testing.T) {
	// Un coureur qui ne fait que des 8 km : l'ancien modèle ne renvoyait rien (8 km hors
	// de toutes les tranches). Toutes les distances doivent maintenant être estimées.
	var runs []RunActivity
	for i := 0; i < 8; i++ {
		runs = append(runs, mkRun(float64(2+i*5), 8, 5.0))
	}
	p := buildRaceForecastAt(runs, refNow)
	for _, id := range []string{"5k", "10k", "half", "marathon"} {
		if legByID(p, id).TimeSec <= 0 {
			t.Fatalf("leg %s non estimée alors que 8 sorties de 8 km sont disponibles", id)
		}
	}
	if p.RunsAnalyzed != 8 {
		t.Fatalf("runs_analyzed = %d, attendu 8", p.RunsAnalyzed)
	}
}

func TestOrderingAndPaceMonotonicity(t *testing.T) {
	var runs []RunActivity
	for i := 0; i < 6; i++ {
		runs = append(runs, mkRun(float64(2+i*6), 10, 5.0))
	}
	p := buildRaceForecastAt(runs, refNow)
	prev := 0.0
	for _, id := range []string{"5k", "10k", "half", "marathon"} {
		l := legByID(p, id)
		if l.PaceSecPerKm <= prev {
			t.Fatalf("allure %s (%v) devrait être plus lente que la distance précédente (%v)", id, l.PaceSecPerKm, prev)
		}
		prev = l.PaceSecPerKm
		if l.TimeLowSec <= 0 || l.TimeHighSec <= l.TimeLowSec {
			t.Fatalf("fourchette %s incohérente : %v–%v", id, l.TimeLowSec, l.TimeHighSec)
		}
		if l.TimeSec < l.TimeLowSec || l.TimeSec > l.TimeHighSec {
			t.Fatalf("temps %s hors de sa fourchette", id)
		}
	}
}

func TestEndurancePenaltyWithoutLongRuns(t *testing.T) {
	// Même allure, mais l'un a des sorties longues et l'autre non : le marathon du second
	// doit être plus lent et moins confiant.
	var short []RunActivity
	for i := 0; i < 6; i++ {
		short = append(short, mkRun(float64(2+i*6), 10, 5.0))
	}
	long := append([]RunActivity{}, short...)
	for i := 0; i < 4; i++ {
		long = append(long, mkRun(float64(5+i*14), 32, 5.6))
	}

	mShort := legByID(buildRaceForecastAt(short, refNow), "marathon")
	mLong := legByID(buildRaceForecastAt(long, refNow), "marathon")

	if mShort.TimeSec <= mLong.TimeSec {
		t.Fatalf("marathon sans sortie longue (%v) devrait être plus lent qu'avec (%v)", mShort.TimeSec, mLong.TimeSec)
	}
	if mShort.Confidence != "low" {
		t.Fatalf("confiance marathon sans sortie longue = %q, attendu low", mShort.Confidence)
	}
	if mShort.DirectRuns != 0 {
		t.Fatalf("direct_runs = %d, attendu 0", mShort.DirectRuns)
	}
	if mShort.RefLegID != "half" && mShort.RefLegID != "10k" {
		t.Fatalf("ref_leg_id = %q, attendu une distance réellement courue", mShort.RefLegID)
	}
}

func TestConfidenceHighWithDirectEvidence(t *testing.T) {
	var runs []RunActivity
	for i := 0; i < 8; i++ {
		runs = append(runs, mkRun(float64(2+i*4), 10.2, 4.8))
	}
	l := legByID(buildRaceForecastAt(runs, refNow), "10k")
	if l.Confidence != "high" {
		t.Fatalf("confiance = %q, attendu high", l.Confidence)
	}
	if l.DataSource != "best_effort" {
		t.Fatalf("data_source = %q", l.DataSource)
	}
	if l.DirectRuns != 8 {
		t.Fatalf("direct_runs = %d, attendu 8", l.DirectRuns)
	}
}

func TestOutOfWindowAndImplausibleRunsIgnored(t *testing.T) {
	runs := []RunActivity{
		mkRun(600, 10, 4.0), // hors fenêtre
		mkRun(10, 10, 1.5),  // 1:30/km : GPS aberrant
		mkRun(10, 10, 25),   // 25:00/km : marche
		mkRun(10, 1.2, 4.0), // trop courte
		mkRun(10, 10, 5.0),  // seule sortie valable
	}
	p := buildRaceForecastAt(runs, refNow)
	if p.RunsAnalyzed != 1 {
		t.Fatalf("runs_analyzed = %d, attendu 1", p.RunsAnalyzed)
	}
	l := legByID(p, "10k")
	if math.Abs(l.PaceSecPerKm-300) > 1 {
		t.Fatalf("allure 10k = %v s/km, attendu ~300", l.PaceSecPerKm)
	}
}

func TestNoDataYieldsInsufficient(t *testing.T) {
	p := buildRaceForecastAt(nil, refNow)
	if p.RunsAnalyzed != 0 {
		t.Fatalf("runs_analyzed = %d", p.RunsAnalyzed)
	}
	for _, l := range p.Legs {
		if l.DataSource != "insufficient_data" || l.TimeSec != 0 {
			t.Fatalf("leg %s devrait être insufficient_data", l.ID)
		}
		if l.Confidence != "low" {
			t.Fatalf("leg %s confiance = %q", l.ID, l.Confidence)
		}
	}
}

func TestHeartRateOnlyFromDirectEvidence(t *testing.T) {
	hr := 158.0
	var runs []RunActivity
	for i := 0; i < 5; i++ {
		r := mkRun(float64(2+i*5), 10, 5.0)
		r.AvgHR = &hr
		runs = append(runs, r)
	}
	p := buildRaceForecastAt(runs, refNow)
	if legByID(p, "10k").TargetHR == nil {
		t.Fatal("le 10k devrait exposer une FC cible (preuve directe)")
	}
	if legByID(p, "marathon").TargetHR != nil {
		t.Fatal("le marathon ne doit pas recopier la FC d'un 10 km")
	}
}
