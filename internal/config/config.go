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
	Grants             map[string]int64
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

	grants, err := parseGrants(os.Getenv("ASSET_GRANTS"))
	if err != nil {
		return Config{}, err
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
		Grants:             grants,
		AllowDirectEntries: allowDirectEntries,
	}, nil
}

func parseGrants(value string) (map[string]int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}

	grants := make(map[string]int64)
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		keyAndValue := strings.SplitN(part, "=", 2)
		if len(keyAndValue) != 2 {
			return nil, fmt.Errorf("ASSET_GRANTS entry %q must use grant_id=delta_minor", part)
		}

		grantID := strings.TrimSpace(keyAndValue[0])
		if !validGrantID(grantID) {
			return nil, fmt.Errorf("ASSET_GRANTS grant id %q is invalid", grantID)
		}

		delta, err := strconv.ParseInt(strings.TrimSpace(keyAndValue[1]), 10, 64)
		if err != nil || delta <= 0 {
			return nil, fmt.Errorf("ASSET_GRANTS grant %q must be a positive integer", grantID)
		}

		grants[grantID] = delta
	}

	return grants, nil
}

func validGrantID(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}

	for _, char := range value {
		if char >= 'a' && char <= 'z' {
			continue
		}
		if char >= 'A' && char <= 'Z' {
			continue
		}
		if char >= '0' && char <= '9' {
			continue
		}
		if char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}

	return true
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
