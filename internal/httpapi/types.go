package httpapi

import (
	"encoding/json"
	"time"

	"github.com/yinlerens/asset-service/internal/store"
)

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type createEntryRequest struct {
	DeltaMinor *int64          `json:"delta_minor"`
	Reason     string          `json:"reason"`
	Metadata   json.RawMessage `json:"metadata"`
}

type accountResponse struct {
	UserID       string    `json:"user_id"`
	BalanceMinor int64     `json:"balance_minor"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type ledgerEntryResponse struct {
	ID                 string          `json:"id"`
	UserID             string          `json:"user_id"`
	IdempotencyKey     string          `json:"idempotency_key"`
	DeltaMinor         int64           `json:"delta_minor"`
	BalanceBeforeMinor int64           `json:"balance_before_minor"`
	BalanceAfterMinor  int64           `json:"balance_after_minor"`
	Reason             string          `json:"reason"`
	Metadata           json.RawMessage `json:"metadata"`
	CreatedAt          time.Time       `json:"created_at"`
}

type ledgerListResponse struct {
	Items      []ledgerEntryResponse `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

type createEntryResponse struct {
	Account           accountResponse     `json:"account"`
	Entry             ledgerEntryResponse `json:"entry"`
	IdempotencyReused bool                `json:"idempotency_reused"`
}

func accountResponseFromStore(account store.Account) accountResponse {
	return accountResponse{
		UserID:       account.UserID.String(),
		BalanceMinor: account.BalanceMinor,
		CreatedAt:    account.CreatedAt,
		UpdatedAt:    account.UpdatedAt,
	}
}

func ledgerEntryResponseFromStore(entry store.LedgerEntry) ledgerEntryResponse {
	metadata := json.RawMessage(entry.Metadata)
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}

	return ledgerEntryResponse{
		ID:                 entry.ID.String(),
		UserID:             entry.UserID.String(),
		IdempotencyKey:     entry.IdempotencyKey,
		DeltaMinor:         entry.DeltaMinor,
		BalanceBeforeMinor: entry.BalanceBeforeMinor,
		BalanceAfterMinor:  entry.BalanceAfterMinor,
		Reason:             entry.Reason,
		Metadata:           metadata,
		CreatedAt:          entry.CreatedAt,
	}
}
