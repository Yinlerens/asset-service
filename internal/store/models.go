package store

import (
	"time"

	"github.com/google/uuid"
)

type Account struct {
	UserID       uuid.UUID
	BalanceMinor int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type LedgerEntry struct {
	ID                 uuid.UUID
	UserID             uuid.UUID
	IdempotencyKey     string
	DeltaMinor         int64
	BalanceBeforeMinor int64
	BalanceAfterMinor  int64
	Reason             string
	Metadata           []byte
	CreatedAt          time.Time
}

type LedgerCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

type CreateEntryInput struct {
	IdempotencyKey string
	DeltaMinor     int64
	Reason         string
	Metadata       []byte
}
