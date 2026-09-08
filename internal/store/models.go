package store

import "time"

// Ledger represents a row in the ledgers hypertable.
type Ledger struct {
	Sequence            uint32    `db:"sequence"`
	Hash                string    `db:"hash"`
	PrevHash            string    `db:"prev_hash"`
	ClosedAt            time.Time `db:"closed_at"`
	TotalCoins          int64     `db:"total_coins"`
	FeePool             int64     `db:"fee_pool"`
	BaseFee             int32     `db:"base_fee"`
	BaseReserve         int32     `db:"base_reserve"`
	MaxTxSetSize        int32     `db:"max_tx_set_size"`
	ProtocolVersion     int32     `db:"protocol_version"`
	TransactionCount    int32     `db:"transaction_count"`
	OperationCount      int32     `db:"operation_count"`
	SuccessfulTxCount   int32     `db:"successful_tx_count"`
	FailedTxCount       int32     `db:"failed_tx_count"`
	TxSetOperationCount *int32    `db:"tx_set_operation_count"`
	HeaderXDR           *string   `db:"header_xdr"`
}

// Transaction represents a row in the transactions hypertable.
type Transaction struct {
	Hash             string    `db:"hash"`
	LedgerSequence   uint32    `db:"ledger_sequence"`
	ApplicationOrder int32     `db:"application_order"`
	Account          string    `db:"account"`
	AccountMuxed     *string   `db:"account_muxed"`
	AccountMuxedID   *int64    `db:"account_muxed_id"`
	AccountSequence  int64     `db:"account_sequence"`
	FeeCharged       int64     `db:"fee_charged"`
	MaxFee           int64     `db:"max_fee"`
	OperationCount   int32     `db:"operation_count"`
	MemoType         int16     `db:"memo_type"`
	MemoText         *string   `db:"memo_text"`
	MemoHash         *string   `db:"memo_hash"`
	Status           int16     `db:"status"`
	IsSoroban        bool      `db:"is_soroban"`
	SorobanResources *string   `db:"soroban_resources"` // JSON
	EnvelopeXDR      string    `db:"envelope_xdr"`
	ResultXDR        string    `db:"result_xdr"`
	ResultMetaXDR    *string   `db:"result_meta_xdr"`
	FeeMetaXDR       *string   `db:"fee_meta_xdr"`
	CreatedAt        time.Time `db:"created_at"`
}

// Operation represents a row in the operations hypertable.
type Operation struct {
	TransactionID      int64     `db:"transaction_id"`
	TransactionHash    string    `db:"transaction_hash"`
	ApplicationOrder   int32     `db:"application_order"`
	Type               int16     `db:"type"`
	TypeName           string    `db:"type_name"`
	SourceAccount      *string   `db:"source_account"`
	SourceAccountMuxed *string   `db:"source_account_muxed"`
	SourceMuxedID      *int64    `db:"source_muxed_id"`
	AssetCode          *string   `db:"asset_code"`
	AssetIssuer        *string   `db:"asset_issuer"`
	Amount             *string   `db:"amount"`
	Destination        *string   `db:"destination"`
	DestinationMuxed   *string   `db:"destination_muxed"`
	DestinationMuxedID *int64    `db:"destination_muxed_id"`
	ContractID         *string   `db:"contract_id"`
	FunctionName       *string   `db:"function_name"`
	Details            string    `db:"details"` // JSON
	CreatedAt          time.Time `db:"created_at"`
}

// Effect represents a row in the effects hypertable.
type Effect struct {
	OperationID     int64     `db:"operation_id"`
	TransactionHash string    `db:"transaction_hash"`
	Type            int16     `db:"type"`
	TypeName        string    `db:"type_name"`
	Account         string    `db:"account"`
	Details         string    `db:"details"` // JSON
	CreatedAt       time.Time `db:"created_at"`
}

// TokenEvent represents a row in the token_events hypertable (CAP-67 unified events).
type TokenEvent struct {
	EventType       int16     `db:"event_type"`      // 0=transfer, 1=mint, 2=burn, 3=clawback, 4=fee
	EventTypeName   string    `db:"event_type_name"` // "transfer", "mint", etc.
	FromAddress     *string   `db:"from_address"`
	FromMuxed       *string   `db:"from_muxed"`
	ToAddress       *string   `db:"to_address"`
	ToMuxed         *string   `db:"to_muxed"`
	ToMuxedID       *int64    `db:"to_muxed_id"`
	AssetType       int16     `db:"asset_type"` // 0=native, 1=credit, 2=soroban_token
	AssetCode       *string   `db:"asset_code"`
	AssetIssuer     *string   `db:"asset_issuer"`
	AssetContractID *string   `db:"asset_contract_id"`
	Amount          string    `db:"amount"`           // i128 as decimal string
	AmountFormatted *string   `db:"amount_formatted"` // formatted with decimals (optional)
	TransactionHash string    `db:"transaction_hash"`
	LedgerSequence  uint32    `db:"ledger_sequence"`
	OperationIndex  *int32    `db:"operation_index"`
	CreatedAt       time.Time `db:"created_at"`
}

// Contract represents a row in the contracts table.
type Contract struct {
	ContractID         string    `db:"contract_id"`
	WasmHash           *string   `db:"wasm_hash"`
	CreatorAccount     *string   `db:"creator_account"`
	CreatedLedger      uint32    `db:"created_ledger"`
	CreatedAt          time.Time `db:"created_at"`
	LastModifiedLedger uint32    `db:"last_modified_ledger"`
	ContractType       int16     `db:"contract_type"` // 0=wasm, 1=stellar_asset, 2=custom
	IsSep41Token       bool      `db:"is_sep41_token"`
	IsSep50NFT         bool      `db:"is_sep50_nft"`
	TokenName          *string   `db:"token_name"`
	TokenSymbol        *string   `db:"token_symbol"`
	TokenDecimals      *int32    `db:"token_decimals"`
	ContractSpec       *string   `db:"contract_spec"` // JSON
}

// ContractCode represents a row in the contract_code table.
type ContractCode struct {
	WasmHash      string    `db:"wasm_hash"`
	WasmBytecode  []byte    `db:"wasm_bytecode"`
	WasmSize      int32     `db:"wasm_size"`
	SpecXDR       *string   `db:"spec_xdr"`    // base64-encoded raw spec XDR
	SpecParsed    *string   `db:"spec_parsed"` // JSON parsed spec
	CreatedLedger uint32    `db:"created_ledger"`
	CreatedAt     time.Time `db:"created_at"`
}

// ContractEvent represents a row in the contract_events hypertable.
type ContractEvent struct {
	ContractID      string    `db:"contract_id"`
	TransactionHash string    `db:"transaction_hash"`
	LedgerSequence  uint32    `db:"ledger_sequence"`
	Type            int16     `db:"type"` // 0=contract, 1=system, 2=diagnostic
	Topic1          *string   `db:"topic_1"`
	Topic2          *string   `db:"topic_2"`
	Topic3          *string   `db:"topic_3"`
	Topic4          *string   `db:"topic_4"`
	TopicsXDR       string    `db:"topics_xdr"`     // base64 encoded
	ValueXDR        string    `db:"value_xdr"`      // base64 encoded
	TopicsDecoded   *string   `db:"topics_decoded"` // JSON
	ValueDecoded    *string   `db:"value_decoded"`  // JSON
	CreatedAt       time.Time `db:"created_at"`
}

const (
	DomainStatusActive  = "active"
	DomainStatusExpired = "expired"
	DomainStatusRevoked = "revoked"

	DomainEventRegister = "register"
	DomainEventTransfer = "transfer"
	DomainEventRenew    = "renew"
	DomainEventRevoke   = "revoke"
	DomainEventClaim    = "claim"

	DomainTargetAccount  = "account"
	DomainTargetContract = "contract"
)

// Domain is the current state of a Soroban Domains registration.
type Domain struct {
	Node            string    `db:"node"`
	Name            string    `db:"name"`
	TLD             string    `db:"tld"`
	Label           string    `db:"label"`
	Owner           string    `db:"owner"`
	ResolvedAddress string    `db:"resolved_address"`
	TargetType      string    `db:"target_type"`
	RegisteredAt    time.Time `db:"registered_at"`
	ExpiresAt       time.Time `db:"expires_at"`
	Status          string    `db:"status"`
	LastEventLedger uint32    `db:"last_event_ledger"`
	LastEventTx     string    `db:"last_event_tx"`
	CreatedAt       time.Time `db:"created_at"`
	UpdatedAt       time.Time `db:"updated_at"`
}

// EffectiveStatus returns the query-facing status: revoked stays revoked,
// otherwise expiry is evaluated at now so stale "active" rows read as expired.
func (d Domain) EffectiveStatus(now time.Time) string {
	if d.Status == DomainStatusRevoked {
		return DomainStatusRevoked
	}
	if !d.ExpiresAt.IsZero() && !d.ExpiresAt.After(now) {
		return DomainStatusExpired
	}
	return DomainStatusActive
}

// DomainEvent is a decoded registry contract event used to update Domain state
// and persisted as per-domain history.
type DomainEvent struct {
	Node            string
	Name            string
	TLD             string
	Label           string
	EventType       string
	Owner           string
	ResolvedAddress string
	ExpiresAt       *time.Time
	RegisteredAt    *time.Time
	TransactionHash string
	LedgerSequence  uint32
	CreatedAt       time.Time
	Details         string // JSON
}

// Verification status values for ContractVerification.Status.
const (
	VerificationStatusPending  = "pending"
	VerificationStatusVerified = "verified"
	VerificationStatusMismatch = "mismatch"
	VerificationStatusFailed   = "failed"
)

// ContractVerification represents a row in the contract_verifications table:
// one submission of source + build metadata for a contract's wasm_hash. It
// is keyed by wasm_hash rather than contract_id so that multiple deployments
// sharing identical bytecode share one verification record.
type ContractVerification struct {
	ID                int64      `db:"id"`
	WasmHash          string     `db:"wasm_hash"`
	ContractID        string     `db:"contract_id"`
	Network           string     `db:"network"`
	RepositoryURL     *string    `db:"repository_url"`
	GitRef            *string    `db:"git_ref"`
	GitCommit         *string    `db:"git_commit"`
	RustVersion       *string    `db:"rust_version"`
	SorobanSDKVersion *string    `db:"soroban_sdk_version"`
	BuildProfile      *string    `db:"build_profile"` // JSON
	Status            string     `db:"status"`
	ComputedWasmHash  *string    `db:"computed_wasm_hash"`
	FailureReason     *string    `db:"failure_reason"`
	BuildLog          *string    `db:"build_log"`
	SubmittedAt       time.Time  `db:"submitted_at"`
	CompletedAt       *time.Time `db:"completed_at"`
	UpdatedAt         time.Time  `db:"updated_at"`
}

// VerificationSourceFile represents a row in the contract_verification_sources
// table: one file of a verified source tree, keyed by verification_id +
// file_path so each submission's snapshot is independently retrievable.
type VerificationSourceFile struct {
	VerificationID int64     `db:"verification_id"`
	WasmHash       string    `db:"wasm_hash"`
	FilePath       string    `db:"file_path"`
	Content        string    `db:"content"`
	SizeBytes      int32     `db:"size_bytes"`
	CreatedAt      time.Time `db:"created_at"`
}
