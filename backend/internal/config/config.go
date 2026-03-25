package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Port               string
	MongoURI           string
	MongoDB            string
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

	frontend := getenv("FRONTEND_URL", "http://localhost:3000")
	c := &Config{
		Port:               getenv("PORT", "8080"),
		MongoURI:           os.Getenv("MONGODB_URI"),
		MongoDB:            getenv("MONGODB_DB", "runapp"),
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
