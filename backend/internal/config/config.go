package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Port               string
	ListenHost         string // LISTEN_HOST : ex. 127.0.0.1 derrière nginx avec network_mode host
	MongoURI           string
	MongoDB            string
	MongoForceIPv4     bool // MONGODB_FORCE_IPV4=1 (opt-in)
	MongoTLS12Only     bool // MONGODB_TLS12_ONLY=1 (opt-in)
	JWTSecret          string
	StravaClientID     string
	StravaClientSecret string
	StravaRedirectURI  string
	FrontendURL        string
	CORSAllowed        []string
	OpenAIAPIKey       string
	OpenAIModel        string
	// AdminEmail : à l’inscription, ce compte reçoit le rôle admin (comparaison insensible à la casse).
	AdminEmail string
	// Stripe : paiement réel des offres. Sans clé secrète, les endpoints de paiement répondent 503
	// et seules les souscriptions à 0 € (promo 100 %) restent possibles.
	StripeSecretKey      string
	StripePublishableKey string
	// StripeWebhookSecret : `whsec_...` — sans lui l’endpoint webhook refuse tout (signature non vérifiable).
	StripeWebhookSecret string
	// ExpoAccessToken : facultatif — requis seulement si « Enhanced Security for Push Notifications »
	// est activé sur le compte Expo. Les notifications admin partent sans lui par défaut.
	ExpoAccessToken string

	// Apple In-App Purchase : encaissement des offres depuis l’app iOS (règle App Store 3.1.1, qui
	// interdit Stripe pour du contenu numérique consommé dans l’app). Stripe reste le circuit du web.
	// Sans ces clés, /checkout/apple/* répond 503 et seul le web peut vendre.
	AppleBundleID string
	// AppleIssuerID / AppleKeyID / AppleKeyP8 : clé « In-App Purchase » de App Store Connect
	// (Users and Access → Integrations → In-App Purchase). Elle signe les appels App Store Server API.
	AppleIssuerID string
	AppleKeyID    string
	// AppleKeyP8 : contenu PEM du .p8. Accepte les retours ligne échappés en \n (pratique en variable
	// d’environnement Docker, qui ne supporte pas le multiligne).
	AppleKeyP8 []byte
	// AppleSandbox : true en développement (achats via compte sandbox App Store Connect).
	AppleSandbox bool
	// AppleProductAllure / AppleProductPerformance : identifiants produit déclarés dans App Store
	// Connect. Ce sont eux qui portent le prix côté iOS — l’admin NeuroRun ne pilote que le web.
	AppleProductAllure      string
	AppleProductPerformance string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	frontend := normalizePrimaryFrontendURL(getenv("FRONTEND_URL", "http://localhost:3000"))
	if frontend == "" {
		frontend = "http://localhost:3000"
	}
	c := &Config{
		Port:               getenv("PORT", "8080"),
		ListenHost:         strings.TrimSpace(os.Getenv("LISTEN_HOST")),
		MongoURI:           os.Getenv("MONGODB_URI"),
		MongoDB:            getenv("MONGODB_DB", "runapp"),
		MongoForceIPv4:     envBool("MONGODB_FORCE_IPV4"),
		MongoTLS12Only:     envBool("MONGODB_TLS12_ONLY"),
		JWTSecret:          os.Getenv("JWT_SECRET"),
		StravaClientID:     os.Getenv("STRAVA_CLIENT_ID"),
		StravaClientSecret: os.Getenv("STRAVA_CLIENT_SECRET"),
		StravaRedirectURI:  os.Getenv("STRAVA_REDIRECT_URI"),
		FrontendURL:        frontend,
		OpenAIAPIKey:       os.Getenv("OPENAI_API_KEY"),
		OpenAIModel:        getenv("OPENAI_MODEL", "gpt-4o"),
		AdminEmail:         strings.TrimSpace(strings.ToLower(os.Getenv("ADMIN_EMAIL"))),

		StripeSecretKey:      strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY")),
		StripePublishableKey: strings.TrimSpace(os.Getenv("STRIPE_PUBLISHABLE_KEY")),
		StripeWebhookSecret:  strings.TrimSpace(os.Getenv("STRIPE_WEBHOOK_SECRET")),

		ExpoAccessToken: strings.TrimSpace(os.Getenv("EXPO_ACCESS_TOKEN")),

		AppleBundleID:           strings.TrimSpace(os.Getenv("APPLE_BUNDLE_ID")),
		AppleIssuerID:           strings.TrimSpace(os.Getenv("APPLE_IAP_ISSUER_ID")),
		AppleKeyID:              strings.TrimSpace(os.Getenv("APPLE_IAP_KEY_ID")),
		AppleKeyP8:              applePrivateKey(os.Getenv("APPLE_IAP_KEY_P8")),
		AppleSandbox:            envBool("APPLE_IAP_SANDBOX"),
		AppleProductAllure:      getenv("APPLE_PRODUCT_ALLURE", "fr.neurorun.app.sub.allure"),
		AppleProductPerformance: getenv("APPLE_PRODUCT_PERFORMANCE", "fr.neurorun.app.sub.performance"),
	}
	if raw := os.Getenv("CORS_ALLOWED_ORIGINS"); raw != "" {
		for _, o := range strings.Split(raw, ",") {
			o = strings.TrimSpace(o)
			if o != "" {
				c.CORSAllowed = append(c.CORSAllowed, o)
			}
		}
	}
	if len(c.CORSAllowed) == 0 {
		c.CORSAllowed = []string{frontend}
	} else {
		// Sinon un .env prod (CORS sans localhost) bloque le front en local.
		c.CORSAllowed = appendOriginIfMissing(c.CORSAllowed, frontend)
	}
	// Next en local utilise http://localhost:3000 (pas https). Souvent FRONTEND_URL pointe la prod
	// pour Strava / liens, donc on autorise aussi ces origines de dev.
	for _, o := range []string{
		"http://localhost:3000",
		"http://127.0.0.1:3000",
		"http://localhost:3001",
		"http://127.0.0.1:3001",
	} {
		c.CORSAllowed = appendOriginIfMissing(c.CORSAllowed, o)
	}

	if c.MongoURI == "" {
		return nil, fmt.Errorf("MONGODB_URI is required — copie backend/.env.example vers backend/.env et renseigne les variables")
	}
	if c.JWTSecret == "" || len(c.JWTSecret) < 16 {
		return nil, fmt.Errorf("JWT_SECRET must be set (min 16 chars)")
	}
	if c.OpenAIAPIKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is required")
	}

	return c, nil
}

// StripeConfigured indique si un vrai paiement peut être créé (clé secrète + clé publique).
func (c *Config) StripeConfigured() bool {
	return c.StripeSecretKey != "" && c.StripePublishableKey != ""
}

// AppleIAPConfigured indique si les achats intégrés iOS peuvent être vérifiés côté serveur.
func (c *Config) AppleIAPConfigured() bool {
	return c.AppleBundleID != "" && c.AppleIssuerID != "" && c.AppleKeyID != "" && len(c.AppleKeyP8) > 0
}

// AppleProductIDs mappe chaque offre payante vers son identifiant produit App Store Connect.
func (c *Config) AppleProductIDs() map[string]string {
	return map[string]string{
		"strava":      c.AppleProductAllure,
		"performance": c.AppleProductPerformance,
	}
}

// applePrivateKey accepte le .p8 tel quel ou avec ses retours ligne échappés en « \n ».
func applePrivateKey(raw string) []byte {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return []byte(strings.ReplaceAll(raw, `\n`, "\n"))
}

// StravaConfigured indique si l’OAuth Strava peut être utilisé (les trois variables doivent être renseignées).
func (c *Config) StravaConfigured() bool {
	return c.StravaClientID != "" && c.StravaClientSecret != "" && c.StravaRedirectURI != ""
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// normalizePrimaryFrontendURL retourne une seule origine pour Strava (redirects HTTP).
// FRONTEND_URL avec plusieurs URLs séparées par des virgules produit sinon une Location
// invalide → page chrome-error:// et « Unsafe attempt to load URL… ».
func normalizePrimaryFrontendURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, ",")
	var trimmed []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			trimmed = append(trimmed, p)
		}
	}
	if len(trimmed) == 0 {
		return ""
	}
	if len(trimmed) == 1 {
		return trimmed[0]
	}
	for _, u := range trimmed {
		low := strings.ToLower(u)
		if strings.HasPrefix(low, "https://") && !strings.Contains(low, "localhost") && !strings.Contains(low, "127.0.0.1") {
			return u
		}
	}
	return trimmed[0]
}

// envBool : vrai si 1, true, yes (insensible à la casse).
func envBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes"
}

func appendOriginIfMissing(origins []string, extra string) []string {
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return origins
	}
	for _, o := range origins {
		if o == extra {
			return origins
		}
	}
	return append(origins, extra)
}
