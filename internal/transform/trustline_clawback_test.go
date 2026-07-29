package transform

// Hermetic unit tests for trustline authorization & clawback operations:
// allow_trust, set_trust_line_flags, clawback, clawback_claimable_balance.
//
// No live network or fixture files are used: each test builds an xdr.Operation
// in memory, calls extractOperationDetails / enrichOperation directly, and
// asserts the expected fields.

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
)

const (
	trustorAddr = "GBKPF4URAGUGPBFKQNMDDD4IY5BRRXRK2VEBULJEMVULCCODND436NIO"
	victimAddr  = "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"
)

func TestExtractOperationDetails_AllowTrust(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeAllowTrust, xdr.AllowTrustOp{
		Trustor:   xdr.MustAddress(trustorAddr),
		Asset:     xdr.MustNewAssetCodeFromString("USDC"),
		Authorize: 1, // AUTHORIZED_FLAG
	})

	details := extractOperationDetails(op)

	assertDetail(t, details, "type", "allow_trust")
	assertDetail(t, details, "trustor", trustorAddr)
	assertDetail(t, details, "asset_code", "USDC")
	assertDetail(t, details, "authorize", "1")
}

func TestEnrichOperation_AllowTrust_NoPromotedColumns(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeAllowTrust, xdr.AllowTrustOp{
		Trustor:   xdr.MustAddress(trustorAddr),
		Asset:     xdr.MustNewAssetCodeFromString("USDT"),
		Authorize: 2,
	})

	details := extractOperationDetails(op)

	storeOp := newStoreOp()
	enrichOperation(&storeOp, op, details)

	// allow_trust does NOT promote to store.Operation columns
	if storeOp.Destination != nil {
		t.Errorf("allow_trust: Destination should be nil, got %v", storeOp.Destination)
	}
	if storeOp.Amount != nil {
		t.Errorf("allow_trust: Amount should be nil, got %v", storeOp.Amount)
	}
	if storeOp.AssetCode != nil {
		t.Errorf("allow_trust: AssetCode should be nil, got %v", storeOp.AssetCode)
	}
	if storeOp.AssetIssuer != nil {
		t.Errorf("allow_trust: AssetIssuer should be nil, got %v", storeOp.AssetIssuer)
	}
}

func TestExtractOperationDetails_SetTrustLineFlags(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeSetTrustLineFlags, xdr.SetTrustLineFlagsOp{
		Trustor:    xdr.MustAddress(trustorAddr),
		Asset:      usdcIssuedAsset(t),
		SetFlags:   1, // AUTHORIZED_FLAG
		ClearFlags: 2, // AUTHORIZED_TO_MAINTAIN_LIABILITIES_FLAG
	})

	details := extractOperationDetails(op)

	assertDetail(t, details, "type", "set_trust_line_flags")
	assertDetail(t, details, "trustor", trustorAddr)
	assertDetail(t, details, "asset", "USDC:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN")
	assertDetail(t, details, "set_flags", "1")
	assertDetail(t, details, "clear_flags", "2")
}

func TestEnrichOperation_SetTrustLineFlags_NoPromotedColumns(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeSetTrustLineFlags, xdr.SetTrustLineFlagsOp{
		Trustor:  xdr.MustAddress(trustorAddr),
		Asset:    usdcIssuedAsset(t),
		SetFlags: 4,
	})

	details := extractOperationDetails(op)

	storeOp := newStoreOp()
	enrichOperation(&storeOp, op, details)

	// set_trust_line_flags does NOT promote to store.Operation columns
	if storeOp.Destination != nil {
		t.Errorf("set_trust_line_flags: Destination should be nil, got %v", storeOp.Destination)
	}
}

func TestExtractOperationDetails_Clawback(t *testing.T) {
	from, err := xdr.AddressToMuxedAccount(victimAddr)
	if err != nil {
		t.Fatalf("failed to build MuxedAccount: %v", err)
	}

	op := decodedOperation(t, xdr.OperationTypeClawback, xdr.ClawbackOp{
		From:   from,
		Asset:  usdcIssuedAsset(t),
		Amount: 10000000,
	})

	details := extractOperationDetails(op)

	assertDetail(t, details, "type", "clawback")
	assertDetail(t, details, "from", victimAddr)
	assertDetail(t, details, "asset", "USDC:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN")
	assertDetail(t, details, "amount", "10000000")
	// Plain G-address: no muxed fields
	assertDetailAbsent(t, details, "from_muxed")
	assertDetailAbsent(t, details, "from_muxed_id")
}

func TestEnrichOperation_Clawback_PromotedColumns(t *testing.T) {
	from, err := xdr.AddressToMuxedAccount(victimAddr)
	if err != nil {
		t.Fatalf("failed to build MuxedAccount: %v", err)
	}

	op := decodedOperation(t, xdr.OperationTypeClawback, xdr.ClawbackOp{
		From:   from,
		Asset:  usdcIssuedAsset(t),
		Amount: 10000000,
	})

	details := extractOperationDetails(op)

	storeOp := &store.Operation{}
	enrichOperation(storeOp, op, details)

	// clawback promotes from→Destination, amount, asset_code, asset_issuer
	if storeOp.Destination == nil || *storeOp.Destination != victimAddr {
		t.Errorf("enriched Destination = %v, want %s", storeOp.Destination, victimAddr)
	}
	if storeOp.Amount == nil || *storeOp.Amount != "10000000" {
		t.Errorf("enriched Amount = %v, want 10000000", storeOp.Amount)
	}
	if storeOp.AssetCode == nil || *storeOp.AssetCode != "USDC" {
		t.Errorf("enriched AssetCode = %v, want USDC", storeOp.AssetCode)
	}
	wantIssuer := "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"
	if storeOp.AssetIssuer == nil || *storeOp.AssetIssuer != wantIssuer {
		t.Errorf("enriched AssetIssuer = %v, want %s", storeOp.AssetIssuer, wantIssuer)
	}
}

func TestExtractOperationDetails_ClawbackClaimableBalance(t *testing.T) {
	var hash xdr.Hash
	for i := range hash {
		hash[i] = 0xcd // distinct from 0xab in other tests
	}
	balanceId := xdr.ClaimableBalanceId{
		Type: xdr.ClaimableBalanceIdTypeClaimableBalanceIdTypeV0,
		V0:   &hash,
	}

	op := decodedOperation(t, xdr.OperationTypeClawbackClaimableBalance, xdr.ClawbackClaimableBalanceOp{
		BalanceId: balanceId,
	})

	details := extractOperationDetails(op)

	assertDetail(t, details, "type", "clawback_claimable_balance")
	// Type-prefixed hex: 4-byte type (0x00000000) + 32-byte hash of 0xcd
	wantId := "00000000cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
	assertDetail(t, details, "balance_id", wantId)
}

func TestEnrichOperation_ClawbackClaimableBalance_NoPromotedColumns(t *testing.T) {
	var hash xdr.Hash
	for i := range hash {
		hash[i] = 0xef
	}
	balanceId := xdr.ClaimableBalanceId{
		Type: xdr.ClaimableBalanceIdTypeClaimableBalanceIdTypeV0,
		V0:   &hash,
	}

	op := decodedOperation(t, xdr.OperationTypeClawbackClaimableBalance, xdr.ClawbackClaimableBalanceOp{
		BalanceId: balanceId,
	})

	details := extractOperationDetails(op)

	storeOp := newStoreOp()
	enrichOperation(&storeOp, op, details)

	// clawback_claimable_balance does NOT promote to store.Operation columns
	if storeOp.Destination != nil {
		t.Errorf("clawback_claimable_balance: Destination should be nil, got %v", storeOp.Destination)
	}
}
