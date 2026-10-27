// Package shoes : catalogue des chaussures de course et calcul de l'usure.
//
// Aucune base publique et gratuite des modèles de running n'existe (seulement
// des jeux de données payants ou des scrapers de boutiques) : le catalogue est
// donc tenu ici, sur les modèles courants. Un modèle absent se saisit à la
// main et prend la durée de vie de sa catégorie.
package shoes

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Catégories de chaussures. Elles fixent la durée de vie par défaut d'un
// modèle saisi à la main.
const (
	CategoryDaily     = "daily"     // polyvalente, entraînement quotidien
	CategoryCushion   = "cushion"   // amorti maximal
	CategoryStability = "stability" // stabilité / pronateur
	CategoryTempo     = "tempo"     // dynamique, allures soutenues
	CategoryRace      = "race"      // compétition, souvent plaque carbone
	CategoryTrail     = "trail"
)

// DefaultLifespanKm : durée de vie théorique d'une catégorie. Fourchettes
// usuelles des fabricants et des tests d'usure : 600 à 800 km pour une
// chaussure d'entraînement, 300 à 400 km pour une chaussure de compétition
// dont la mousse très souple se tasse plus vite.
var DefaultLifespanKm = map[string]float64{
	CategoryDaily:     700,
	CategoryCushion:   750,
	CategoryStability: 750,
	CategoryTempo:     550,
	CategoryRace:      350,
	CategoryTrail:     650,
}

// LifespanFor : durée de vie par défaut d'une catégorie ; polyvalente si la
// catégorie est inconnue.
func LifespanFor(category string) float64 {
	if km, ok := DefaultLifespanKm[category]; ok {
		return km
	}
	return DefaultLifespanKm[CategoryDaily]
}

// ValidCategory : la catégorie fait-elle partie de la liste ?
func ValidCategory(c string) bool {
	_, ok := DefaultLifespanKm[c]
	return ok
}

// Model : un modèle du catalogue.
type Model struct {
	ID         string  `json:"id"`
	Brand      string  `json:"brand"`
	Name       string  `json:"model"`
	Category   string  `json:"category"`
	LifespanKm float64 `json:"lifespan_km"`
}

type entry struct {
	brand, name, category string
	// km : durée de vie propre au modèle ; 0 = celle de la catégorie.
	km float64
}

var entries = []entry{
	{"Nike", "Pegasus 41", CategoryDaily, 700},
	{"Nike", "Pegasus 42", CategoryDaily, 700},
	{"Nike", "Vomero 18", CategoryCushion, 750},
	{"Nike", "Invincible 3", CategoryCushion, 650},
	{"Nike", "Structure 26", CategoryStability, 700},
	{"Nike", "Zoom Fly 6", CategoryTempo, 500},
	{"Nike", "Vaporfly 3", CategoryRace, 300},
	{"Nike", "Alphafly 3", CategoryRace, 350},
	{"Nike", "Pegasus Trail 5", CategoryTrail, 650},

	{"Adidas", "Adizero SL2", CategoryDaily, 600},
	{"Adidas", "Adizero Evo SL", CategoryTempo, 550},
	{"Adidas", "Adizero Boston 12", CategoryTempo, 600},
	{"Adidas", "Adizero Adios Pro 4", CategoryRace, 350},
	{"Adidas", "Supernova Rise 2", CategoryDaily, 700},
	{"Adidas", "Ultraboost 5", CategoryCushion, 700},
	{"Adidas", "Terrex Agravic Speed", CategoryTrail, 600},

	{"Asics", "Gel-Nimbus 27", CategoryCushion, 800},
	{"Asics", "Gel-Cumulus 27", CategoryDaily, 750},
	{"Asics", "Novablast 5", CategoryDaily, 650},
	{"Asics", "Gel-Kayano 32", CategoryStability, 800},
	{"Asics", "GT-2000 13", CategoryStability, 750},
	{"Asics", "Superblast 2", CategoryTempo, 650},
	{"Asics", "Magic Speed 4", CategoryTempo, 450},
	{"Asics", "Metaspeed Sky Paris", CategoryRace, 350},
	{"Asics", "Metaspeed Edge Paris", CategoryRace, 350},
	{"Asics", "Trabuco Max 4", CategoryTrail, 700},
	{"Asics", "Gel-Trabuco 13", CategoryTrail, 700},

	{"Hoka", "Clifton 10", CategoryDaily, 650},
	{"Hoka", "Bondi 9", CategoryCushion, 750},
	{"Hoka", "Skyward X", CategoryCushion, 650},
	{"Hoka", "Arahi 8", CategoryStability, 700},
	{"Hoka", "Mach 6", CategoryTempo, 550},
	{"Hoka", "Rocket X 2", CategoryRace, 350},
	{"Hoka", "Cielo X1 3.0", CategoryRace, 300},
	{"Hoka", "Speedgoat 6", CategoryTrail, 650},
	{"Hoka", "Mafate Speed 4", CategoryTrail, 700},

	{"Brooks", "Ghost 17", CategoryDaily, 750},
	{"Brooks", "Glycerin 22", CategoryCushion, 750},
	{"Brooks", "Adrenaline GTS 24", CategoryStability, 750},
	{"Brooks", "Hyperion Max 2", CategoryTempo, 550},
	{"Brooks", "Hyperion Elite 4", CategoryRace, 350},
	{"Brooks", "Cascadia 18", CategoryTrail, 750},
	{"Brooks", "Caldera 8", CategoryTrail, 650},

	{"Saucony", "Ride 18", CategoryDaily, 700},
	{"Saucony", "Triumph 23", CategoryCushion, 750},
	{"Saucony", "Guide 18", CategoryStability, 700},
	{"Saucony", "Kinvara 15", CategoryTempo, 500},
	{"Saucony", "Endorphin Speed 5", CategoryTempo, 550},
	{"Saucony", "Endorphin Pro 4", CategoryRace, 350},
	{"Saucony", "Endorphin Elite 2", CategoryRace, 300},
	{"Saucony", "Peregrine 15", CategoryTrail, 650},

	{"New Balance", "Fresh Foam X 880 v15", CategoryDaily, 750},
	{"New Balance", "Fresh Foam X 1080 v14", CategoryCushion, 750},
	{"New Balance", "Fresh Foam X More v5", CategoryCushion, 700},
	{"New Balance", "Fresh Foam X 860 v14", CategoryStability, 750},
	{"New Balance", "FuelCell Rebel v4", CategoryTempo, 500},
	{"New Balance", "FuelCell SC Trainer v3", CategoryTempo, 600},
	{"New Balance", "FuelCell SC Elite v4", CategoryRace, 350},
	{"New Balance", "Fresh Foam X Hierro v9", CategoryTrail, 650},

	{"On", "Cloudsurfer 2", CategoryDaily, 600},
	{"On", "Cloudmonster 2", CategoryCushion, 600},
	{"On", "Cloudeclipse", CategoryCushion, 650},
	{"On", "Cloudrunner 2", CategoryStability, 650},
	{"On", "Cloudflow 4", CategoryTempo, 500},
	{"On", "Cloudboom Strike", CategoryRace, 350},
	{"On", "Cloudultra 2", CategoryTrail, 650},

	{"Puma", "Velocity Nitro 3", CategoryDaily, 700},
	{"Puma", "MagMax Nitro", CategoryCushion, 700},
	{"Puma", "Deviate Nitro 3", CategoryTempo, 650},
	{"Puma", "Deviate Nitro Elite 3", CategoryRace, 400},
	{"Puma", "Fast-R Nitro Elite 3", CategoryRace, 350},

	{"Mizuno", "Wave Rider 28", CategoryDaily, 750},
	{"Mizuno", "Wave Sky 8", CategoryCushion, 750},
	{"Mizuno", "Wave Inspire 21", CategoryStability, 750},
	{"Mizuno", "Neo Vista 2", CategoryTempo, 550},
	{"Mizuno", "Wave Rebellion Pro 3", CategoryRace, 350},

	{"Salomon", "Aero Glide 2", CategoryDaily, 650},
	{"Salomon", "Speedcross 6", CategoryTrail, 700},
	{"Salomon", "Sense Ride 5", CategoryTrail, 650},
	{"Salomon", "Genesis", CategoryTrail, 650},
	{"Salomon", "S/Lab Ultra Glide", CategoryTrail, 600},

	{"Kiprun", "KS900 2", CategoryDaily, 700},
	{"Kiprun", "KS500 2", CategoryDaily, 700},
	{"Kiprun", "KD800", CategoryTempo, 600},
	{"Kiprun", "KD900", CategoryTempo, 600},
	{"Kiprun", "KD900X LD+", CategoryRace, 400},
	{"Kiprun", "MT Cushion 2", CategoryTrail, 650},

	{"Altra", "Torin 8", CategoryDaily, 650},
	{"Altra", "Via Olympus 2", CategoryCushion, 700},
	{"Altra", "Lone Peak 9", CategoryTrail, 650},

	{"La Sportiva", "Bushido III", CategoryTrail, 700},
	{"La Sportiva", "Prodigio", CategoryTrail, 650},
}

var catalog = buildCatalog()

func buildCatalog() []Model {
	out := make([]Model, 0, len(entries))
	for _, e := range entries {
		km := e.km
		if km <= 0 {
			km = LifespanFor(e.category)
		}
		out = append(out, Model{
			ID:         slug(e.brand + " " + e.name),
			Brand:      e.brand,
			Name:       e.name,
			Category:   e.category,
			LifespanKm: km,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Brand != out[j].Brand {
			return out[i].Brand < out[j].Brand
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Catalog : tous les modèles, triés par marque puis par nom.
func Catalog() []Model {
	return catalog
}

// Find : modèle du catalogue par identifiant.
func Find(id string) (Model, bool) {
	for _, m := range catalog {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// slug : « New Balance FuelCell SC Elite v4 » → « new-balance-fuelcell-sc-elite-v4 ».
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
