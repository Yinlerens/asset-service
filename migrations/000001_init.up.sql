create schema if not exists asset;

create table if not exists asset.accounts (
  user_id uuid primary key,
  balance_minor bigint not null default 0,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint accounts_balance_non_negative check (balance_minor >= 0)
);

create table if not exists asset.ledger_entries (
  id uuid primary key,
  user_id uuid not null references asset.accounts(user_id) on delete restrict,
  idempotency_key text not null,
  delta_minor bigint not null,
  balance_before_minor bigint not null,
  balance_after_minor bigint not null,
  reason text not null default '',
  metadata jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  constraint ledger_entries_delta_non_zero check (delta_minor <> 0),
  constraint ledger_entries_balance_before_non_negative check (balance_before_minor >= 0),
  constraint ledger_entries_balance_after_non_negative check (balance_after_minor >= 0),
  constraint ledger_entries_idempotency_key_length check (
    length(idempotency_key) > 0 and length(idempotency_key) <= 128
  ),
  constraint ledger_entries_reason_length check (length(reason) <= 200),
  constraint ledger_entries_metadata_object check (jsonb_typeof(metadata) = 'object'),
  constraint ledger_entries_idempotency_unique unique (user_id, idempotency_key)
);

create index if not exists ledger_entries_user_created_idx
  on asset.ledger_entries (user_id, created_at desc, id desc);
