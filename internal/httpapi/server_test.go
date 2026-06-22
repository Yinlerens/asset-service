package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yinlerens/asset-service/internal/store"
)

type fakeStore struct {
	account store.Account
	entries []store.LedgerEntry
}

func (f *fakeStore) Ping(ctx context.Context) error {
	return nil
}

func (f *fakeStore) EnsureAccount(ctx context.Context, userID uuid.UUID) (store.Account, error) {
	f.account.UserID = userID
	return f.account, nil
}

func (f *fakeStore) ListLedgerEntries(ctx context.Context, userID uuid.UUID, cursor *store.LedgerCursor, limit int) ([]store.LedgerEntry, error) {
	return f.entries, nil
}

func (f *fakeStore) CreateEntry(ctx context.Context, userID uuid.UUID, input store.CreateEntryInput) (store.LedgerEntry, store.Account, bool, error) {
	for _, entry := range f.entries {
		if entry.UserID == userID && entry.IdempotencyKey == input.IdempotencyKey {
			f.account.UserID = userID
			return entry, f.account, true, nil
		}
	}

	entry := store.LedgerEntry{
		ID:                 uuid.New(),
		UserID:             userID,
		IdempotencyKey:     input.IdempotencyKey,
		DeltaMinor:         input.DeltaMinor,
		BalanceBeforeMinor: f.account.BalanceMinor,
		BalanceAfterMinor:  f.account.BalanceMinor + input.DeltaMinor,
		Reason:             input.Reason,
		Metadata:           input.Metadata,
		CreatedAt:          time.Now().UTC(),
	}
	f.account.UserID = userID
	f.account.BalanceMinor = entry.BalanceAfterMinor
	f.entries = append(f.entries, entry)
	return entry, f.account, false, nil
}

func TestGetAccountRequiresGatewayAuth(t *testing.T) {
	api := New(&fakeStore{}, Options{InternalToken: "secret"})
	request := httptest.NewRequest(http.MethodGet, "/v1/me/account", nil)
	response := httptest.NewRecorder()

	api.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

func TestGetAccountEnsuresAccountForGatewayUser(t *testing.T) {
	userID := uuid.New()
	createdAt := time.Unix(1710000000, 0).UTC()
	api := New(&fakeStore{
		account: store.Account{
			BalanceMinor: 2500,
			CreatedAt:    createdAt,
			UpdatedAt:    createdAt,
		},
	}, Options{InternalToken: "secret"})

	request := httptest.NewRequest(http.MethodGet, "/v1/me/account", nil)
	request.Header.Set(internalTokenHeader, "secret")
	request.Header.Set(userIDHeader, userID.String())
	response := httptest.NewRecorder()

	api.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}

	var body accountResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.UserID != userID.String() {
		t.Fatalf("expected user_id %s, got %s", userID, body.UserID)
	}
	if body.BalanceMinor != 2500 {
		t.Fatalf("expected balance 2500, got %d", body.BalanceMinor)
	}
}

func TestCreateEntryRequiresIdempotencyKey(t *testing.T) {
	userID := uuid.New()
	api := New(&fakeStore{}, Options{InternalToken: "secret", AllowDirectEntries: true})

	request := httptest.NewRequest(http.MethodPost, "/v1/me/entries", strings.NewReader(`{"delta_minor":100}`))
	request.Header.Set(internalTokenHeader, "secret")
	request.Header.Set(userIDHeader, userID.String())
	response := httptest.NewRecorder()

	api.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestCreateEntryRejectsNonObjectMetadata(t *testing.T) {
	userID := uuid.New()
	api := New(&fakeStore{}, Options{InternalToken: "secret", AllowDirectEntries: true})

	request := httptest.NewRequest(http.MethodPost, "/v1/me/entries", strings.NewReader(`{"delta_minor":100,"metadata":[]}`))
	request.Header.Set(internalTokenHeader, "secret")
	request.Header.Set(userIDHeader, userID.String())
	request.Header.Set(idempotencyHeader, "entry-1")
	response := httptest.NewRecorder()

	api.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestCreateEntryIsDisabledByDefault(t *testing.T) {
	userID := uuid.New()
	api := New(&fakeStore{}, Options{InternalToken: "secret"})

	request := httptest.NewRequest(http.MethodPost, "/v1/me/entries", strings.NewReader(`{"delta_minor":100}`))
	request.Header.Set(internalTokenHeader, "secret")
	request.Header.Set(userIDHeader, userID.String())
	request.Header.Set(idempotencyHeader, "entry-1")
	response := httptest.NewRecorder()

	api.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.Code)
	}
}

func TestCreateCreditRequiresIdempotencyKey(t *testing.T) {
	userID := uuid.New()
	api := New(&fakeStore{}, Options{InternalToken: "secret"})

	request := httptest.NewRequest(http.MethodPost, "/v1/me/credits", strings.NewReader(`{"amount_minor":100}`))
	request.Header.Set(internalTokenHeader, "secret")
	request.Header.Set(userIDHeader, userID.String())
	response := httptest.NewRecorder()

	api.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestCreateCreditCreditsCurrentUser(t *testing.T) {
	userID := uuid.New()
	fake := &fakeStore{}
	api := New(fake, Options{InternalToken: "secret"})

	for attempt := 1; attempt <= 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/v1/me/credits", strings.NewReader(`{"amount_minor":16000,"reason":"manual_credit","metadata":{"source":"frontend"}}`))
		request.Header.Set(internalTokenHeader, "secret")
		request.Header.Set(userIDHeader, userID.String())
		request.Header.Set(idempotencyHeader, "credit-1")
		response := httptest.NewRecorder()

		api.Handler().ServeHTTP(response, request)

		expectedStatus := http.StatusCreated
		if attempt == 2 {
			expectedStatus = http.StatusOK
		}
		if response.Code != expectedStatus {
			t.Fatalf("attempt %d: expected %d, got %d", attempt, expectedStatus, response.Code)
		}

		var body createCreditResponse
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatalf("attempt %d: decode response: %v", attempt, err)
		}
		if body.Account.BalanceMinor != 16000 {
			t.Fatalf("attempt %d: expected balance 16000, got %d", attempt, body.Account.BalanceMinor)
		}
		if body.Entry.IdempotencyKey != "credit:credit-1" {
			t.Fatalf("attempt %d: expected credit idempotency key, got %s", attempt, body.Entry.IdempotencyKey)
		}
		if body.Entry.Reason != "manual_credit" {
			t.Fatalf("attempt %d: expected manual_credit reason, got %s", attempt, body.Entry.Reason)
		}
		if body.IdempotencyReused != (attempt == 2) {
			t.Fatalf("attempt %d: unexpected idempotency_reused=%v", attempt, body.IdempotencyReused)
		}
	}
}

func TestCreateCreditRejectsNonPositiveAmount(t *testing.T) {
	for _, payload := range []string{`{"amount_minor":0}`, `{"amount_minor":-100}`} {
		userID := uuid.New()
		api := New(&fakeStore{}, Options{InternalToken: "secret"})

		request := httptest.NewRequest(http.MethodPost, "/v1/me/credits", strings.NewReader(payload))
		request.Header.Set(internalTokenHeader, "secret")
		request.Header.Set(userIDHeader, userID.String())
		request.Header.Set(idempotencyHeader, "credit-1")
		response := httptest.NewRecorder()

		api.Handler().ServeHTTP(response, request)

		if response.Code != http.StatusBadRequest {
			t.Fatalf("payload %s: expected 400, got %d", payload, response.Code)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	entry := store.LedgerEntry{
		ID:        uuid.New(),
		CreatedAt: time.Unix(1710000000, 123).UTC(),
	}

	cursor := encodeCursor(entry)
	parsed, err := parseCursor(cursor)
	if err != nil {
		t.Fatalf("parse cursor: %v", err)
	}

	if parsed.ID != entry.ID {
		t.Fatalf("expected id %s, got %s", entry.ID, parsed.ID)
	}
	if !parsed.CreatedAt.Equal(entry.CreatedAt) {
		t.Fatalf("expected created_at %s, got %s", entry.CreatedAt, parsed.CreatedAt)
	}
}
