package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	defaultPort           = "8080"
	defaultMaxLedgerLimit = 100
)

type Config struct {
	Addr           string
	DatabaseURL    string
	InternalToken  string
	MaxLedgerLimit int
}

func Load() (Config, error) {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = defaultPort
	}

	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}

	internalToken := strings.TrimSpace(os.Getenv("INTERNAL_TOKEN"))
	if internalToken == "" {
		return Config{}, errors.New("INTERNAL_TOKEN is required")
	}

	maxLedgerLimit := defaultMaxLedgerLimit
	if value := strings.TrimSpace(os.Getenv("MAX_LEDGER_LIMIT")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			return Config{}, fmt.Errorf("MAX_LEDGER_LIMIT must be a positive integer")
		}
		maxLedgerLimit = parsed
	}

	return Config{
		Addr:           ":" + port,
		DatabaseURL:    databaseURL,
		InternalToken:  internalToken,
		MaxLedgerLimit: maxLedgerLimit,
	}, nil
}
