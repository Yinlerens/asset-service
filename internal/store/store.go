package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/exaring/otelpgx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInsufficientFunds   = errors.New("insufficient funds")
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	ErrBalanceOverflow     = errors.New("balance overflow")
	ErrAccountNotFound     = errors.New("account not found")
	ErrLedgerEntryNotFound = errors.New("ledger entry not found")
)

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, databaseURL string) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	config.ConnConfig.Tracer = otelpgx.NewTracer()

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pg pool: %w", err)
	}

	store := &Store{pool: pool}
	if err := store.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}

func (s *Store) EnsureAccount(ctx context.Context, userID uuid.UUID) (Account, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Account{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollback(ctx, tx)

	account, err := ensureAccount(ctx, tx, userID)
	if err != nil {
		return Account{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Account{}, fmt.Errorf("commit transaction: %w", err)
	}

	return account, nil
}

func (s *Store) GetAccount(ctx context.Context, userID uuid.UUID) (Account, error) {
	account, err := scanAccount(s.pool.QueryRow(ctx, `
		select user_id, balance_minor, created_at, updated_at
		from asset.accounts
		where user_id = $1
	`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	if err != nil {
		return Account{}, err
	}
	return account, nil
}

func (s *Store) ListLedgerEntries(ctx context.Context, userID uuid.UUID, cursor *LedgerCursor, limit int) ([]LedgerEntry, error) {
	var (
		rows pgx.Rows
		err  error
	)

	if cursor == nil {
		rows, err = s.pool.Query(ctx, `
			select id, user_id, idempotency_key, delta_minor, balance_before_minor,
			       balance_after_minor, reason, metadata, created_at
			from asset.ledger_entries
			where user_id = $1
			order by created_at desc, id desc
			limit $2
		`, userID, limit)
	} else {
		rows, err = s.pool.Query(ctx, `
			select id, user_id, idempotency_key, delta_minor, balance_before_minor,
			       balance_after_minor, reason, metadata, created_at
			from asset.ledger_entries
			where user_id = $1
			  and (created_at, id) < ($2, $3)
			order by created_at desc, id desc
			limit $4
		`, userID, cursor.CreatedAt, cursor.ID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("query ledger entries: %w", err)
	}
	defer rows.Close()

	entries := make([]LedgerEntry, 0, limit)
	for rows.Next() {
		entry, err := scanLedgerEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ledger entries: %w", err)
	}

	return entries, nil
}

func (s *Store) GetLedgerEntryByIdempotencyKey(ctx context.Context, userID uuid.UUID, idempotencyKey string) (LedgerEntry, error) {
	entry, err := scanLedgerEntry(s.pool.QueryRow(ctx, `
		select id, user_id, idempotency_key, delta_minor, balance_before_minor,
		       balance_after_minor, reason, metadata, created_at
		from asset.ledger_entries
		where user_id = $1 and idempotency_key = $2
	`, userID, idempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return LedgerEntry{}, ErrLedgerEntryNotFound
	}
	if err != nil {
		return LedgerEntry{}, err
	}
	return entry, nil
}

func (s *Store) CreateEntry(ctx context.Context, userID uuid.UUID, input CreateEntryInput) (LedgerEntry, Account, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return LedgerEntry{}, Account{}, false, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollback(ctx, tx)

	account, err := ensureAccountLocked(ctx, tx, userID)
	if err != nil {
		return LedgerEntry{}, Account{}, false, err
	}

	existing, err := findEntryByIdempotencyKey(ctx, tx, userID, input.IdempotencyKey)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return LedgerEntry{}, Account{}, false, err
	}
	if err == nil {
		if existing.DeltaMinor != input.DeltaMinor ||
			existing.Reason != input.Reason ||
			!jsonEqual(existing.Metadata, input.Metadata) {
			return LedgerEntry{}, Account{}, false, ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return LedgerEntry{}, Account{}, false, fmt.Errorf("commit transaction: %w", err)
		}
		return existing, account, true, nil
	}

	newBalance, ok := addInt64(account.BalanceMinor, input.DeltaMinor)
	if !ok {
		return LedgerEntry{}, Account{}, false, ErrBalanceOverflow
	}
	if newBalance < 0 {
		return LedgerEntry{}, Account{}, false, ErrInsufficientFunds
	}

	entry := LedgerEntry{
		ID:                 uuid.New(),
		UserID:             userID,
		IdempotencyKey:     input.IdempotencyKey,
		DeltaMinor:         input.DeltaMinor,
		BalanceBeforeMinor: account.BalanceMinor,
		BalanceAfterMinor:  newBalance,
		Reason:             input.Reason,
		Metadata:           input.Metadata,
	}

	entry, err = insertLedgerEntry(ctx, tx, entry)
	if err != nil {
		return LedgerEntry{}, Account{}, false, err
	}

	account, err = updateAccountBalance(ctx, tx, userID, newBalance)
	if err != nil {
		return LedgerEntry{}, Account{}, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return LedgerEntry{}, Account{}, false, fmt.Errorf("commit transaction: %w", err)
	}

	return entry, account, false, nil
}

func ensureAccount(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (Account, error) {
	if _, err := tx.Exec(ctx, `
		insert into asset.accounts (user_id)
		values ($1)
		on conflict (user_id) do nothing
	`, userID); err != nil {
		return Account{}, fmt.Errorf("ensure account: %w", err)
	}

	return scanAccount(tx.QueryRow(ctx, `
		select user_id, balance_minor, created_at, updated_at
		from asset.accounts
		where user_id = $1
	`, userID))
}

func ensureAccountLocked(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (Account, error) {
	if _, err := tx.Exec(ctx, `
		insert into asset.accounts (user_id)
		values ($1)
		on conflict (user_id) do nothing
	`, userID); err != nil {
		return Account{}, fmt.Errorf("ensure account: %w", err)
	}

	return scanAccount(tx.QueryRow(ctx, `
		select user_id, balance_minor, created_at, updated_at
		from asset.accounts
		where user_id = $1
		for update
	`, userID))
}

func findEntryByIdempotencyKey(ctx context.Context, tx pgx.Tx, userID uuid.UUID, idempotencyKey string) (LedgerEntry, error) {
	return scanLedgerEntry(tx.QueryRow(ctx, `
		select id, user_id, idempotency_key, delta_minor, balance_before_minor,
		       balance_after_minor, reason, metadata, created_at
		from asset.ledger_entries
		where user_id = $1 and idempotency_key = $2
	`, userID, idempotencyKey))
}

func insertLedgerEntry(ctx context.Context, tx pgx.Tx, entry LedgerEntry) (LedgerEntry, error) {
	return scanLedgerEntry(tx.QueryRow(ctx, `
		insert into asset.ledger_entries (
			id, user_id, idempotency_key, delta_minor, balance_before_minor,
			balance_after_minor, reason, metadata
		)
		values ($1, $2, $3, $4, $5, $6, $7, $8)
		returning id, user_id, idempotency_key, delta_minor, balance_before_minor,
		          balance_after_minor, reason, metadata, created_at
	`, entry.ID, entry.UserID, entry.IdempotencyKey, entry.DeltaMinor,
		entry.BalanceBeforeMinor, entry.BalanceAfterMinor, entry.Reason, entry.Metadata))
}

func updateAccountBalance(ctx context.Context, tx pgx.Tx, userID uuid.UUID, balanceMinor int64) (Account, error) {
	return scanAccount(tx.QueryRow(ctx, `
		update asset.accounts
		set balance_minor = $2,
		    updated_at = now()
		where user_id = $1
		returning user_id, balance_minor, created_at, updated_at
	`, userID, balanceMinor))
}

func scanAccount(row pgx.Row) (Account, error) {
	var account Account
	if err := row.Scan(&account.UserID, &account.BalanceMinor, &account.CreatedAt, &account.UpdatedAt); err != nil {
		return Account{}, fmt.Errorf("scan account: %w", err)
	}
	return account, nil
}

type ledgerScanner interface {
	Scan(dest ...any) error
}

func scanLedgerEntry(row ledgerScanner) (LedgerEntry, error) {
	var entry LedgerEntry
	if err := row.Scan(
		&entry.ID,
		&entry.UserID,
		&entry.IdempotencyKey,
		&entry.DeltaMinor,
		&entry.BalanceBeforeMinor,
		&entry.BalanceAfterMinor,
		&entry.Reason,
		&entry.Metadata,
		&entry.CreatedAt,
	); err != nil {
		return LedgerEntry{}, fmt.Errorf("scan ledger entry: %w", err)
	}
	return entry, nil
}

func rollback(ctx context.Context, tx pgx.Tx) {
	_ = tx.Rollback(ctx)
}

func jsonEqual(left []byte, right []byte) bool {
	leftValue, leftOK := decodeJSON(left)
	rightValue, rightOK := decodeJSON(right)
	return leftOK && rightOK && reflect.DeepEqual(leftValue, rightValue)
}

func decodeJSON(value []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()

	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, false
	}

	return decoded, true
}

func addInt64(left int64, right int64) (int64, bool) {
	if right > 0 && left > math.MaxInt64-right {
		return 0, false
	}
	if right < 0 && left < math.MinInt64-right {
		return 0, false
	}
	return left + right, true
}
