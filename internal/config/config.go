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
	Addr               string
	DatabaseURL        string
	InternalToken      string
	MaxLedgerLimit     int
	AllowDirectEntries bool
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

	allowDirectEntries, err := boolEnv("ALLOW_DIRECT_ENTRIES")
	if err != nil {
		return Config{}, err
	}

	return Config{
		Addr:               ":" + port,
		DatabaseURL:        databaseURL,
		InternalToken:      internalToken,
		MaxLedgerLimit:     maxLedgerLimit,
		AllowDirectEntries: allowDirectEntries,
	}, nil
}

func boolEnv(name string) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return false, nil
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}

	return parsed, nil
}
