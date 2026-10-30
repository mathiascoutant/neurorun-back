// Package weather : la météo d'une course (Open-Meteo, gratuit et sans clé) et
// ce qu'elle coûte en allure.
package weather

import "math"

// Conditions : la météo à l'heure d'une course, et le ralentissement qu'elle
// impose.
type Conditions struct {
	TempC       float64 `json:"temp_c" bson:"temp_c"`
	HumidityPct float64 `json:"humidity_pct" bson:"humidity_pct"`
	DewPointC   float64 `json:"dew_point_c" bson:"dew_point_c"`
	// WindKmh : vent moyen à 10 m, en km/h (0 si la source ne le donne pas).
	WindKmh float64 `json:"wind_kmh,omitempty" bson:"wind_kmh,omitempty"`
	// HeatPct : ralentissement d'allure attendu à effort égal, en pour cent.
	// Zéro par temps frais.
	HeatPct float64 `json:"heat_pct" bson:"heat_pct"`
	// ColdPct, WindPct : ralentissement dû au froid et au vent, en %.
	ColdPct float64 `json:"cold_pct,omitempty" bson:"cold_pct,omitempty"`
	WindPct float64 `json:"wind_pct,omitempty" bson:"wind_pct,omitempty"`
	// SlowdownPct : ralentissement total (chaleur + froid + vent), plafonné.
	SlowdownPct float64 `json:"slowdown_pct,omitempty" bson:"slowdown_pct,omitempty"`
	// Code : état du ciel, code météo WMO (0 dégagé, 61 pluie, 95 orage…).
	// Ne ralentit rien, il se lit seulement. Absent quand la source ne le
	// donne pas : 0 voulant dire « dégagé », d'où le pointeur.
	Code *int `json:"weather_code,omitempty" bson:"weather_code,omitempty"`
	// IsDay : faux la nuit, pour dire « nuit claire » plutôt que « ensoleillé ».
	IsDay *bool `json:"is_day,omitempty" bson:"is_day,omitempty"`
}

// heatTable : ralentissement selon la somme température + point de rosée, en
// degrés Fahrenheit — le repère des coachs américains (Hadley), le plus simple
// qui tienne compte de l'humidité : la sueur ne refroidit plus quand l'air est
// déjà chargé d'eau. Jusqu'à 100 °F, rien ; au-delà de 180, courir vite n'a
// plus de sens et on plafonne.
//
// Repères : 18 °C / 50 % → 0 ; 25 °C / 60 % → 2,9 % ; 29 °C / 70 % → 5,6 %
// (5:05 devient 5:22) ; 33 °C / 60 % → 7,4 %.
var heatTable = [][2]float64{
	{100, 0}, {110, 0.5}, {120, 1}, {130, 2}, {140, 3},
	{150, 4.5}, {160, 6}, {170, 8}, {180, 10},
}

const (
	// maxHeatPct : au-delà de la table, on ne corrige plus davantage.
	maxHeatPct = 12.0
	// minHeatPct : en deçà, l'effet se perd dans le bruit d'une sortie — on ne
	// corrige pas une note pour un après-midi doux.
	minHeatPct = 1.0
)

// HeatSlowdownPct : ralentissement d'allure à prévoir, en %.
func HeatSlowdownPct(tempC, dewPointC float64) float64 {
	sum := toF(tempC) + toF(dewPointC)
	first, last := heatTable[0], heatTable[len(heatTable)-1]
	if sum <= first[0] {
		return 0
	}
	var pct float64
	if sum >= last[0] {
		// Même pente que la dernière tranche, jusqu'au plafond.
		pct = math.Min(maxHeatPct, last[1]+(sum-last[0])*0.2)
	} else {
		for i := 1; i < len(heatTable); i++ {
			a, b := heatTable[i-1], heatTable[i]
			if sum <= b[0] {
				pct = a[1] + (sum-a[0])/(b[0]-a[0])*(b[1]-a[1])
				break
			}
		}
	}
	if pct < minHeatPct {
		return 0
	}
	return round1(pct)
}

// DewPoint : point de rosée (formule de Magnus), quand la source n'en donne pas.
func DewPoint(tempC, humidityPct float64) float64 {
	const a, b = 17.62, 243.12
	rh := math.Min(math.Max(humidityPct, 1), 100) / 100
	g := math.Log(rh) + a*tempC/(b+tempC)
	return b * g / (a - g)
}

// NewConditions : les conditions complètes à partir de la température et de
// l'humidité, point de rosée calculé si absent. Sans vent : voir WithWind.
func NewConditions(tempC, humidityPct float64, dewPointC *float64) Conditions {
	dp := DewPoint(tempC, humidityPct)
	if dewPointC != nil {
		dp = *dewPointC
	}
	c := Conditions{
		TempC:       round1(tempC),
		HumidityPct: math.Round(humidityPct),
		DewPointC:   round1(dp),
		HeatPct:     HeatSlowdownPct(tempC, dp),
		ColdPct:     ColdSlowdownPct(tempC),
	}
	c.SlowdownPct = c.total()
	return c
}

// WithWind : les mêmes conditions, avec le vent mesuré.
func (c Conditions) WithWind(windKmh float64) Conditions {
	c.WindKmh = math.Round(math.Max(0, windKmh))
	c.WindPct = WindSlowdownPct(windKmh)
	c.SlowdownPct = c.total()
	return c
}

func toF(c float64) float64 { return c*9/5 + 32 }

func round1(x float64) float64 { return math.Round(x*10) / 10 }
