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
