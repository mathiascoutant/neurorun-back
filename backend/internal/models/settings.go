package models

// TierFeatures : interrupteurs par offre (standard / strava / performance), modifiables depuis l’admin.
type TierFeatures struct {
	CoachChat       bool `json:"coach_chat" bson:"coach_chat"`
	StravaDashboard bool `json:"strava_dashboard" bson:"strava_dashboard"`
	Goals           bool `json:"goals" bson:"goals"`
	LiveRuns        bool `json:"live_runs" bson:"live_runs"`
	Forecast        bool `json:"forecast" bson:"forecast"`
	Circuit         bool `json:"circuit" bson:"circuit"`
}

// OfferConfig : document unique (clé fixe côté store) — prix + flags par palier.
type OfferConfig struct {
	Tiers     map[string]TierFeatures `json:"tiers" bson:"tiers"`
	PricesEUR map[string]float64      `json:"prices_eur" bson:"prices_eur"`
}

// DefaultOfferConfig : valeurs par défaut si aucun document en base.
func DefaultOfferConfig() OfferConfig {
	return OfferConfig{
		Tiers: map[string]TierFeatures{
			"standard": {
				CoachChat: true, StravaDashboard: false, Goals: true, LiveRuns: true,
				Forecast: false, Circuit: false,
			},
			"strava": {
				CoachChat: true, StravaDashboard: true, Goals: true, LiveRuns: true,
				Forecast: true, Circuit: false,
			},
			"performance": {
				CoachChat: true, StravaDashboard: true, Goals: true, LiveRuns: true,
				Forecast: true, Circuit: true,
			},
		},
		PricesEUR: map[string]float64{
			"strava":      3.99,
			"performance": 7.99,
		},
	}
}

func (c *OfferConfig) MergeDefaults() {
	def := DefaultOfferConfig()
	if c.Tiers == nil {
		c.Tiers = make(map[string]TierFeatures)
	}
	for k, v := range def.Tiers {
		if _, ok := c.Tiers[k]; !ok {
			c.Tiers[k] = v
		}
	}
	if c.PricesEUR == nil {
		c.PricesEUR = make(map[string]float64)
	}
	for k, v := range def.PricesEUR {
		if _, ok := c.PricesEUR[k]; !ok {
			c.PricesEUR[k] = v
		}
	}
}

// CapabilitiesForPlan retourne les flags effectifs pour un abonnement utilisateur.
func (c *OfferConfig) CapabilitiesForPlan(plan string) map[string]bool {
	c.MergeDefaults()
	if plan == "" {
		plan = PlanStandard
	}
	t, ok := c.Tiers[plan]
	if !ok {
		t = c.Tiers[PlanStandard]
	}
	return map[string]bool{
		"coach_chat":       t.CoachChat,
		"strava_dashboard": t.StravaDashboard,
		"goals":            t.Goals,
		"live_runs":        t.LiveRuns,
		"forecast":         t.Forecast,
		"circuit":          t.Circuit,
	}
}
