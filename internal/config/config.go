package config

import (
	"os"
	"strconv"
)

type Config struct {
	Port         int
	Host         string
	CCABaseURL   string
	DefaultModel string
	UserAgent    string
	StaticToken  string
}

func Load() *Config {
	port := 9876
	if pStr := os.Getenv("PORT"); pStr != "" {
		if p, err := strconv.Atoi(pStr); err == nil {
			port = p
		}
	}

	host := "127.0.0.1"
	if h := os.Getenv("HOST"); h != "" {
		host = h
	}

	ccaBase := "https://daily-cloudcode-pa.googleapis.com"
	if b := os.Getenv("CCA_BASE_URL"); b != "" {
		ccaBase = b
	}

	// Optional static token fallback from environment so no login flow is required
	token := os.Getenv("ANTIGRAVITY_TOKEN")
	if token == "" {
		token = os.Getenv("GOOGLE_OAUTH_TOKEN")
	}

	return &Config{
		Port:         port,
		Host:         host,
		CCABaseURL:   ccaBase,
		DefaultModel: "gemini-3.8-flash",
		UserAgent:    "antigravity",
		StaticToken:  token,
	}
}
