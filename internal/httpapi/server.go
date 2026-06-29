package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yinlerens/asset-service/internal/store"
)

const (
	internalTokenHeader = "X-Internal-Token"
	userIDHeader        = "X-User-Id"
	idempotencyHeader   = "Idempotency-Key"
)

type Store interface {
	Ping(ctx context.Context) error
	EnsureAccount(ctx context.Context, userID uuid.UUID) (store.Account, error)
	ListLedgerEntries(ctx context.Context, userID uuid.UUID, cursor *store.LedgerCursor, limit int) ([]store.LedgerEntry, error)
	CreateEntry(ctx context.Context, userID uuid.UUID, input store.CreateEntryInput) (store.LedgerEntry, store.Account, bool, error)
}

type Options struct {
	InternalToken      string
	MaxLedgerLimit     int
	AllowDirectEntries bool
}

type Server struct {
	store              Store
	internalToken      string
	maxLedgerLimit     int
	allowDirectEntries bool
}

func New(store Store, opts Options) *Server {
	maxLedgerLimit := opts.MaxLedgerLimit
	if maxLedgerLimit < 1 {
		maxLedgerLimit = 100
	}

	return &Server{
		store:              store,
		internalToken:      opts.InternalToken,
		maxLedgerLimit:     maxLedgerLimit,
		allowDirectEntries: opts.AllowDirectEntries,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.Handle("GET /v1/me/account", s.withGatewayAuth(http.HandlerFunc(s.handleGetAccount)))
	mux.Handle("GET /v1/me/ledger", s.withGatewayAuth(http.HandlerFunc(s.handleListLedger)))
	mux.Handle("POST /v1/me/credits", s.withGatewayAuth(http.HandlerFunc(s.handleCreateCredit)))
	mux.Handle("POST /v1/me/spends", s.withGatewayAuth(http.HandlerFunc(s.handleCreateSpend)))
	if s.allowDirectEntries {
		mux.Handle("POST /v1/me/entries", s.withGatewayAuth(http.HandlerFunc(s.handleCreateEntry)))
	}
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := s.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "database is unavailable")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	userID := mustUserID(r.Context())

	account, err := s.store.EnsureAccount(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "account_unavailable", "account is unavailable")
		return
	}

	writeJSON(w, http.StatusOK, accountResponseFromStore(account))
}

func (s *Server) handleListLedger(w http.ResponseWriter, r *http.Request) {
	userID := mustUserID(r.Context())

	limit, err := parseLimit(r, s.maxLedgerLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}

	cursor, err := parseCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "cursor is invalid")
		return
	}

	entries, err := s.store.ListLedgerEntries(r.Context(), userID, cursor, limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ledger_unavailable", "ledger is unavailable")
		return
	}

	var nextCursor string
	if len(entries) > limit {
		entries = entries[:limit]
		nextCursor = encodeCursor(entries[len(entries)-1])
	}

	response := ledgerListResponse{
		Items:      make([]ledgerEntryResponse, 0, len(entries)),
		NextCursor: nextCursor,
	}
	for _, entry := range entries {
		response.Items = append(response.Items, ledgerEntryResponseFromStore(entry))
	}

	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleCreateEntry(w http.ResponseWriter, r *http.Request) {
	userID := mustUserID(r.Context())

	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}

	var request createEntryRequest
	if err := decodeRequestJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	if request.DeltaMinor == nil || *request.DeltaMinor == 0 {
		writeError(w, http.StatusBadRequest, "invalid_delta", "delta_minor is required and must be non-zero")
		return
	}

	reason := strings.TrimSpace(request.Reason)
	if len(reason) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_reason", "reason must be 200 characters or fewer")
		return
	}

	metadata, err := normalizeMetadata(request.Metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_metadata", err.Error())
		return
	}

	entry, account, reused, err := s.store.CreateEntry(r.Context(), userID, store.CreateEntryInput{
		IdempotencyKey: idempotencyKey,
		DeltaMinor:     *request.DeltaMinor,
		Reason:         reason,
		Metadata:       metadata,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrInsufficientFunds):
			writeError(w, http.StatusConflict, "insufficient_funds", "balance cannot go below zero")
		case errors.Is(err, store.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used with a different request")
		case errors.Is(err, store.ErrBalanceOverflow):
			writeError(w, http.StatusConflict, "balance_overflow", "balance is outside the supported range")
		default:
			writeError(w, http.StatusInternalServerError, "entry_unavailable", "entry could not be created")
		}
		return
	}

	writeJSON(w, http.StatusCreated, createEntryResponse{
		Account:           accountResponseFromStore(account),
		Entry:             ledgerEntryResponseFromStore(entry),
		IdempotencyReused: reused,
	})
}

func (s *Server) handleCreateCredit(w http.ResponseWriter, r *http.Request) {
	userID := mustUserID(r.Context())

	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}

	var request createCreditRequest
	if err := decodeRequestJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	if request.AmountMinor == nil || *request.AmountMinor <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_amount", "amount_minor is required and must be greater than zero")
		return
	}

	reason := strings.TrimSpace(request.Reason)
	if reason == "" {
		reason = "asset_credit"
	}
	if len(reason) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_reason", "reason must be 200 characters or fewer")
		return
	}

	metadata, err := normalizeMetadata(request.Metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_metadata", err.Error())
		return
	}

	entry, account, reused, err := s.store.CreateEntry(r.Context(), userID, store.CreateEntryInput{
		IdempotencyKey: "credit:" + idempotencyKey,
		DeltaMinor:     *request.AmountMinor,
		Reason:         reason,
		Metadata:       metadata,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used with a different request")
		case errors.Is(err, store.ErrBalanceOverflow):
			writeError(w, http.StatusConflict, "balance_overflow", "balance is outside the supported range")
		default:
			writeError(w, http.StatusInternalServerError, "credit_unavailable", "asset credit could not be created")
		}
		return
	}

	statusCode := http.StatusCreated
	if reused {
		statusCode = http.StatusOK
	}

	writeJSON(w, statusCode, createCreditResponse{
		Account:           accountResponseFromStore(account),
		Entry:             ledgerEntryResponseFromStore(entry),
		IdempotencyReused: reused,
	})
}

func (s *Server) handleCreateSpend(w http.ResponseWriter, r *http.Request) {
	userID := mustUserID(r.Context())

	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}

	var request createSpendRequest
	if err := decodeRequestJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	if request.AmountMinor == nil || *request.AmountMinor <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_amount", "amount_minor is required and must be greater than zero")
		return
	}

	reason := strings.TrimSpace(request.Reason)
	if reason == "" {
		reason = "asset_spend"
	}
	if len(reason) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_reason", "reason must be 200 characters or fewer")
		return
	}

	metadata, err := normalizeMetadata(request.Metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_metadata", err.Error())
		return
	}

	entry, account, reused, err := s.store.CreateEntry(r.Context(), userID, store.CreateEntryInput{
		IdempotencyKey: "spend:" + idempotencyKey,
		DeltaMinor:     -*request.AmountMinor,
		Reason:         reason,
		Metadata:       metadata,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrInsufficientFunds):
			writeError(w, http.StatusConflict, "insufficient_funds", "balance cannot go below zero")
		case errors.Is(err, store.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used with a different request")
		case errors.Is(err, store.ErrBalanceOverflow):
			writeError(w, http.StatusConflict, "balance_overflow", "balance is outside the supported range")
		default:
			writeError(w, http.StatusInternalServerError, "spend_unavailable", "asset spend could not be created")
		}
		return
	}

	statusCode := http.StatusCreated
	if reused {
		statusCode = http.StatusOK
	}

	writeJSON(w, statusCode, createSpendResponse{
		Account:           accountResponseFromStore(account),
		Entry:             ledgerEntryResponseFromStore(entry),
		IdempotencyReused: reused,
	})
}

func (s *Server) withGatewayAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !constantTimeEqual(r.Header.Get(internalTokenHeader), s.internalToken) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "request is not authorized")
			return
		}

		userIDValue := strings.TrimSpace(r.Header.Get(userIDHeader))
		userID, err := uuid.Parse(userIDValue)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_user_id", "X-User-Id must be a UUID")
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDContextKey{}, userID)))
	})
}

func constantTimeEqual(left string, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func readIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	idempotencyKey := strings.TrimSpace(r.Header.Get(idempotencyHeader))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "missing_idempotency_key", "Idempotency-Key header is required")
		return "", false
	}
	if len(idempotencyKey) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be 128 characters or fewer")
		return "", false
	}

	return idempotencyKey, true
}

type userIDContextKey struct{}

func mustUserID(ctx context.Context) uuid.UUID {
	userID, ok := ctx.Value(userIDContextKey{}).(uuid.UUID)
	if !ok {
		panic("user id missing from request context")
	}
	return userID
}

func parseLimit(r *http.Request, maxLimit int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return min(50, maxLimit), nil
	}

	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		return 0, fmt.Errorf("limit must be a positive integer")
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	return limit, nil
}

func parseCursor(value string) (*store.LedgerCursor, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}

	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid cursor")
	}

	createdAtUnixNano, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, err
	}

	id, err := uuid.Parse(parts[1])
	if err != nil {
		return nil, err
	}

	return &store.LedgerCursor{
		CreatedAt: time.Unix(0, createdAtUnixNano).UTC(),
		ID:        id,
	}, nil
}

func encodeCursor(entry store.LedgerEntry) string {
	value := fmt.Sprintf("%d|%s", entry.CreatedAt.UTC().UnixNano(), entry.ID.String())
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeRequestJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request body must contain a single JSON object")
	}

	return nil
}

func normalizeMetadata(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return json.RawMessage(`{}`), nil
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("metadata must be valid JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("metadata must be a single JSON object")
	}

	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("metadata must be a JSON object")
	}

	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("metadata could not be encoded")
	}

	return normalized, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, errorResponse{
		Error: apiError{
			Code:    code,
			Message: message,
		},
	})
}
