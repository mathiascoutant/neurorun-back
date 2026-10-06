package strava

import (
	"math"
	"slices"
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

// mkRace crée une course déclarée comme telle sur Strava.
func mkRace(daysAgo, km, paceMinKm float64) RunActivity {
	r := mkRun(daysAgo, km, paceMinKm)
	r.WorkoutType = WorkoutTypeRace
	return r
}

func withHR(r RunActivity, bpm float64) RunActivity {
	r.AvgHR = &bpm
	return r
}

func legByID(p RaceForecastPayload, id string) RaceLegForecast {
	for _, l := range p.Legs {
		if l.ID == id {
			return l
		}
	}
	return RaceLegForecast{}
}

// assertTime vérifie un chrono à `tolSec` secondes près.
func assertTime(t *testing.T, what string, got, want, tolSec float64) {
	t.Helper()
	if math.Abs(got-want) > tolSec {
		t.Fatalf("%s = %s, attendu %s (±%.0f s)", what, fmtSec(got), fmtSec(want), tolSec)
	}
}

func fmtSec(s float64) string {
	return time.Duration(math.Round(s) * float64(time.Second)).String()
}

// easyVolume : un gros volume de footings à 4:40–5:00/km, de 5 à 8 km, étalés
// sur six mois — le quotidien d'un coureur régulier.
func easyVolume() []RunActivity {
	var runs []RunActivity
	for i := 0; i < 80; i++ {
		km := 5 + float64(i%4)
		pace := 4.67 + 0.05*float64(i%5)
		runs = append(runs, mkRun(float64(1+i*2), km, pace))
	}
	return runs
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

func TestRiegelExponentByRange(t *testing.T) {
	// Un simple doublement (5 → 10 km, 10 km → semi) garde l'exposant classique.
	if got := riegelExponent(race5kKm, race10kKm); got != riegelPower {
		t.Fatalf("exposant 5→10 km = %v, attendu %v", got, riegelPower)
	}
	if got := riegelExponent(race10kKm, raceHalfKm); math.Abs(got-riegelPower) > 0.001 {
		t.Fatalf("exposant 10 km→semi = %v, attendu ≈ %v", got, riegelPower)
	}
	// Le marathon est pénalisé, même depuis le semi.
	if got := riegelExponent(raceHalfKm, raceMarathonKm); got <= riegelPower {
		t.Fatalf("exposant semi→marathon = %v, devrait dépasser %v", got, riegelPower)
	}
	if got := riegelExponent(3, raceMarathonKm); got > riegelMaxPower {
		t.Fatalf("exposant %v au-delà du plafond %v", got, riegelMaxPower)
	}
}

// Le cas qui a motivé la refonte : un coureur qui fait surtout des footings et
// quelques courses à fond. L'ancien moteur prenait un percentile de TOUTES ses
// sorties et prévoyait ~22:40 sur 5 km pour un coureur à 19:00.
func TestRacesBeatEasyTrainingVolume(t *testing.T) {
	runs := append(easyVolume(),
		mkRace(20, 5, 3.80),  // 19:00
		mkRace(45, 5, 3.85),  // 19:15
		mkRace(90, 10, 3.95), // 39:30
		mkRace(130, 5, 3.90),
	)
	p := buildRaceForecastAt(runs, refNow)

	five := legByID(p, "5k")
	assertTime(t, "5 km", five.TimeSec, 19*60+5, 15)
	if five.Confidence != "high" {
		t.Fatalf("confiance 5 km = %q, attendu high", five.Confidence)
	}
	if five.Ref == nil || !five.Ref.Race || five.Ref.DistanceKm != 5 {
		t.Fatalf("la référence du 5 km doit être la course récente, got %+v", five.Ref)
	}
	if five.TimeLowSec > 19*60 || five.TimeHighSec < 19*60+15 {
		t.Fatalf("fourchette %s–%s : doit encadrer le niveau réel", fmtSec(five.TimeLowSec), fmtSec(five.TimeHighSec))
	}

	// 10 km : le 5 km récent (Riegel 1.06) et le 10 km d'il y a 3 mois concordent.
	assertTime(t, "10 km", legByID(p, "10k").TimeSec, 39*60+40, 30)
}

func TestAncientPeakFades(t *testing.T) {
	// Un 10 km en 40:00 il y a 300 jours ne vaut plus 40:00 aujourd'hui…
	runs := []RunActivity{mkRace(300, 10, 4.0)}
	for i := 0; i < 10; i++ {
		runs = append(runs, mkRun(float64(3+i*3), 10, 5.0))
	}
	l := legByID(buildRaceForecastAt(runs, refNow), "10k")
	want := 40 * 60 * formDecay(300)
	assertTime(t, "10 km", l.TimeSec, want, 2)
	if l.TimeSec < 43*60 || l.TimeSec > 44*60 {
		t.Fatalf("10 km = %s : l'érosion de forme (≈ 8,6 %%) n'est pas appliquée", fmtSec(l.TimeSec))
	}
	if l.Confidence != "low" {
		t.Fatalf("confiance = %q : une référence de 10 mois ne peut pas être fiable", l.Confidence)
	}

	// … et au-delà d'un an, elle sort de la fenêtre.
	runs[0] = mkRace(400, 10, 4.0)
	l = legByID(buildRaceForecastAt(runs, refNow), "10k")
	assertTime(t, "10 km sans le vieux record", l.TimeSec, 50*60, 2)
}

func TestRecentPerformanceIsNotDecayed(t *testing.T) {
	runs := []RunActivity{mkRace(30, 10, 4.0)}
	l := legByID(buildRaceForecastAt(runs, refNow), "10k")
	assertTime(t, "10 km", l.TimeSec, 40*60, 1)
}

func TestGPSOutlierIgnored(t *testing.T) {
	var runs []RunActivity
	for i := 0; i < 10; i++ {
		runs = append(runs, mkRun(float64(3+i*4), 10, 4.9+0.02*float64(i)))
	}
	// 3:30/km sur 10 km au milieu de sorties à 5:00 : trajet à vélo ou GPS fou.
	runs = append(runs, mkRun(8, 10, 3.5))
	l := legByID(buildRaceForecastAt(runs, refNow), "10k")
	if l.TimeSec < 48*60 {
		t.Fatalf("10 km = %s : la sortie aberrante n'a pas été écartée", fmtSec(l.TimeSec))
	}
}

func TestDeclaredRaceOutlierKept(t *testing.T) {
	var runs []RunActivity
	for i := 0; i < 10; i++ {
		runs = append(runs, mkRun(float64(3+i*4), 10, 5.0))
	}
	// Une vraie course 17 % plus rapide que les footings : c'est le niveau du coureur.
	runs = append(runs, mkRace(8, 10, 4.15))
	l := legByID(buildRaceForecastAt(runs, refNow), "10k")
	assertTime(t, "10 km", l.TimeSec, 41*60+30, 2)
}

func TestRaceNameAttestsUntaggedRace(t *testing.T) {
	var runs []RunActivity
	for i := 0; i < 10; i++ {
		runs = append(runs, mkRun(float64(3+i*4), 5, 5.0))
	}
	r := mkRun(8, 5, 4.2)
	r.Name = "P.12 — 🥉 M1"
	runs = append(runs, r)
	l := legByID(buildRaceForecastAt(runs, refNow), "5k")
	assertTime(t, "5 km", l.TimeSec, 21*60, 2)
}

func TestHardHeartRateAttestsUntaggedEffort(t *testing.T) {
	build := func(effortHR float64) RaceLegForecast {
		var runs []RunActivity
		for i := 0; i < 10; i++ {
			runs = append(runs, withHR(mkRun(float64(3+i*4), 10, 5.0), 140+float64(i)))
		}
		effort := mkRun(8, 10, 4.3) // 14 % plus rapide que tout le reste
		if effortHR > 0 {
			effort = withHR(effort, effortHR)
		}
		return legByID(buildRaceForecastAt(append(runs, effort), refNow), "10k")
	}
	// À 178 bpm, l'effort est attesté : c'est le niveau du coureur.
	assertTime(t, "10 km (FC d'effort)", build(178).TimeSec, 43*60, 2)
	// Sans cardio ni déclaration de course, rien ne distingue cet écart d'un GPS qui dérive.
	if got := build(0).TimeSec; got < 49*60 {
		t.Fatalf("10 km = %s : un écart de 14 %% non attesté doit être écarté", fmtSec(got))
	}
}

func TestRaceDistanceSnappedToOfficial(t *testing.T) {
	// GPS à 10,07 km pour un 10 km étalonné couru en 38:39 : le chrono officiel compte.
	race := mkRace(10, 10.07, 38.65/10.07)
	l := legByID(buildRaceForecastAt([]RunActivity{race}, refNow), "10k")
	assertTime(t, "10 km recalé", l.TimeSec, 38*60+39, 1)
	if l.Ref == nil || l.Ref.DistanceKm != 10 {
		t.Fatalf("la référence doit afficher la distance officielle, got %+v", l.Ref)
	}

	// Sortie non déclarée : la distance GPS est conservée.
	free := mkRun(10, 10.07, 38.65/10.07)
	l = legByID(buildRaceForecastAt([]RunActivity{free}, refNow), "10k")
	if l.TimeSec >= 38*60+39 {
		t.Fatalf("10 km = %s : une sortie libre ne doit pas être recalée", fmtSec(l.TimeSec))
	}
}

func TestTreadmillOnlyWithoutOutdoorEvidence(t *testing.T) {
	tm := mkRun(5, 5, 3.6) // capteur de foulée mal étalonné
	tm.Trainer = true
	outdoor := mkRun(6, 5, 5.0)

	l := legByID(buildRaceForecastAt([]RunActivity{tm, outdoor}, refNow), "5k")
	assertTime(t, "5 km", l.TimeSec, 25*60, 1)

	l = legByID(buildRaceForecastAt([]RunActivity{tm}, refNow), "5k")
	assertTime(t, "5 km (tapis seul)", l.TimeSec, 18*60, 1)
}

func TestConsensusSmoothsSingleLuckyDay(t *testing.T) {
	runs := []RunActivity{
		mkRace(10, 5, 4.00), // 20:00
		mkRace(20, 5, 4.04), // 20:12
		mkRace(30, 5, 4.06), // 20:18
	}
	l := legByID(buildRaceForecastAt(runs, refNow), "5k")
	if l.TimeSec <= 20*60 || l.TimeSec >= 20*60+12 {
		t.Fatalf("5 km = %s : attendu entre la meilleure course et la suivante", fmtSec(l.TimeSec))
	}
	if l.SupportRuns != 3 {
		t.Fatalf("support_runs = %d, attendu 3", l.SupportRuns)
	}
}

func TestTrainingAnchorRangeOpensTowardFaster(t *testing.T) {
	var runs []RunActivity
	for i := 0; i < 6; i++ {
		runs = append(runs, mkRun(float64(3+i*5), 10, 5.0))
	}
	l := legByID(buildRaceForecastAt(runs, refNow), "10k")
	below := l.TimeSec - l.TimeLowSec
	above := l.TimeHighSec - l.TimeSec
	if below <= above {
		t.Fatalf("fourchette −%.0f s / +%.0f s : sans course, le coureur peut faire mieux que ses sorties", below, above)
	}
}

func TestRunsOutsideLegacyBucketsAreUsed(t *testing.T) {
	// Un coureur qui ne fait que des 8 km : toutes les distances doivent être estimées.
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
	if legByID(p, "marathon").Confidence != "low" {
		t.Fatal("un marathon projeté depuis des 8 km ne peut pas être fiable")
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
	if mShort.RefLegID != "10k" {
		t.Fatalf("ref_leg_id = %q, attendu la distance réellement courue", mShort.RefLegID)
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
		if l.Ref != nil {
			t.Fatalf("leg %s sans donnée ne doit pas avoir de référence", l.ID)
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

func TestTargetHRComesFromEffortsNotFootings(t *testing.T) {
	var runs []RunActivity
	for i := 0; i < 20; i++ {
		runs = append(runs, withHR(mkRun(float64(2+i*3), 5, 5.0), 140))
	}
	runs = append(runs,
		withHR(mkRace(10, 5, 4.0), 178),
		withHR(mkRace(30, 5, 4.05), 176),
		withHR(mkRace(50, 5, 4.02), 132), // capteur en défaut sur une course
	)
	l := legByID(buildRaceForecastAt(runs, refNow), "5k")
	if l.TargetHR == nil || *l.TargetHR < 170 {
		t.Fatalf("FC visée = %v : elle doit venir des courses, pas des footings", l.TargetHR)
	}
}

// mkIntervalRun crée une séance à intervalles : `paceMinKm` est sa moyenne, qui
// additionne efforts et récupérations et ne mesure donc aucune allure tenue.
func mkIntervalRun(daysAgo, km, paceMinKm float64) RunActivity {
	r := mkRun(daysAgo, km, paceMinKm)
	r.Name = "Fractionné"
	r.WorkoutType = WorkoutTypeRunWorkout
	return r
}

// Une séance à intervalles ne doit pas peser sur la projection : sa moyenne est
// lente par construction, alors qu'elle prouve le contraire d'une baisse de forme.
func TestIntervalRunsExcludedFromForecast(t *testing.T) {
	var base []RunActivity
	for i := 0; i < 10; i++ {
		base = append(base, mkRun(float64(4+i*10), 10, 4.75+0.05*float64(i)))
	}
	withIntervals := append(slices.Clone(base), []RunActivity{
		mkIntervalRun(6, 8, 4.0),
		mkIntervalRun(13, 8, 6.10),
		mkIntervalRun(20, 7.7, 6.05),
	}...)

	ref := legByID(buildRaceForecastAt(base, refNow), "10k")
	got := legByID(buildRaceForecastAt(withIntervals, refNow), "10k")

	if ref.TimeSec <= 0 {
		t.Fatal("projection de référence vide")
	}
	if math.Abs(got.TimeSec-ref.TimeSec) > 1 {
		t.Fatalf(
			"projection 10k = %.0f s avec les fractionnés, %.0f s sans : écart de %.0f s",
			got.TimeSec, ref.TimeSec, got.TimeSec-ref.TimeSec,
		)
	}
	if got.SampleRuns != ref.SampleRuns {
		t.Fatalf("SampleRuns = %d, attendu %d : les fractionnés comptent encore", got.SampleRuns, ref.SampleRuns)
	}
}

// Une course déclarée reste une course, même si son titre ressemble à un fractionné.
func TestDeclaredRaceWithRepsNameIsNotAnInterval(t *testing.T) {
	r := mkRace(10, 7, 4.2)
	r.Name = "Relais 2 x 7 km"
	p := buildRaceForecastAt([]RunActivity{r}, refNow)
	if p.IntervalsExcluded != 0 || p.RunsAnalyzed != 1 {
		t.Fatalf("relais écarté comme fractionné (exclus=%d, analysées=%d)", p.IntervalsExcluded, p.RunsAnalyzed)
	}
}

// Le volume couru en fractionné reste une preuve d'endurance : l'écarter du calcul
// d'allure ne doit pas faire retomber le garde-fou sortie longue.
func TestIntervalRunsStillCountAsDistanceCovered(t *testing.T) {
	runs := []RunActivity{
		mkRun(5, 10, 5.0),
		mkRun(12, 10, 5.1),
		mkRun(19, 10, 5.05),
		mkIntervalRun(9, 18, 6.2),
	}
	p := buildRaceForecastAt(runs, refNow)
	if math.Abs(p.LongestRunKm-18) > 0.01 {
		t.Fatalf("LongestRunKm = %.2f, attendu 18 (le fractionné a bien été couru)", p.LongestRunKm)
	}
}

func TestIsRaceName(t *testing.T) {
	cases := map[string]bool{
		"RP 💥 18’42":                  true,
		"P.15 — 🏆 M2":                 true,
		"PODIUM SCRATCH ! 🥉 Youpiiii": true,
		"Parkrun de Bordeaux":         true,
		"Corrida des Étoiles":         true,
		"Compétition régionale":       true,
		"Course à pied le matin":      false,
		"Footing récup":               false,
		"Sortie longue":               false,
		"Préparation printemps":       false,
		"":                            false,
	}
	for name, want := range cases {
		if got := IsRaceName(name); got != want {
			t.Errorf("IsRaceName(%q) = %v, attendu %v", name, got, want)
		}
	}
}
