package transform

import (
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
)

const (
	claimantAddr  = "GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H"
	sponsoredAddr = "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"
)

func testBalanceId(t *testing.T) xdr.ClaimableBalanceId {
	t.Helper()
	var hash xdr.Hash
	for i := range hash {
		hash[i] = 0xab
	}
	return xdr.ClaimableBalanceId{
		Type: xdr.ClaimableBalanceIdTypeClaimableBalanceIdTypeV0,
		V0:   &hash,
	}
}

func TestExtractOperationDetails_CreateClaimableBalance(t *testing.T) {
	absBefore := xdr.Int64(1700000000)
	op := decodedOperation(t, xdr.OperationTypeCreateClaimableBalance, xdr.CreateClaimableBalanceOp{
		Asset:  usdcIssuedAsset(t),
		Amount: 5000000,
		Claimants: []xdr.Claimant{
			{
				Type: xdr.ClaimantTypeClaimantTypeV0,
				V0: &xdr.ClaimantV0{
					Destination: xdr.MustAddress(claimantAddr),
					Predicate: xdr.ClaimPredicate{
						Type: xdr.ClaimPredicateTypeClaimPredicateUnconditional,
					},
				},
			},
			{
				Type: xdr.ClaimantTypeClaimantTypeV0,
				V0: &xdr.ClaimantV0{
					Destination: xdr.MustAddress(sponsoredAddr),
					Predicate: xdr.ClaimPredicate{
						Type:      xdr.ClaimPredicateTypeClaimPredicateBeforeAbsoluteTime,
						AbsBefore: &absBefore,
					},
				},
			},
		},
	})

	details := extractOperationDetails(op)

	want := map[string]interface{}{
		"type":   "create_claimable_balance",
		"asset":  "USDC:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN",
		"amount": "5000000",
	}
	for k, v := range want {
		if got := details[k]; got != v {
			t.Errorf("details[%q] = %v, want %v", k, got, v)
		}
	}

	claimants, ok := details["claimants"].([]map[string]interface{})
	if !ok {
		t.Fatalf("details[\"claimants\"] has type %T, want []map[string]interface{}", details["claimants"])
	}
	if len(claimants) != 2 {
		t.Fatalf("len(claimants) = %d, want 2", len(claimants))
	}
	if got := claimants[0]["destination"]; got != claimantAddr {
		t.Errorf("claimants[0].destination = %v, want %v", got, claimantAddr)
	}
	pred0, ok := claimants[0]["predicate"].(map[string]interface{})
	if !ok || pred0["unconditional"] != true {
		t.Errorf("claimants[0].predicate = %v, want unconditional", claimants[0]["predicate"])
	}
	pred1, ok := claimants[1]["predicate"].(map[string]interface{})
	if !ok || pred1["abs_before"] != "1700000000" {
		t.Errorf("claimants[1].predicate = %v, want abs_before 1700000000", claimants[1]["predicate"])
	}

	storeOp := &store.Operation{}
	enrichOperation(storeOp, op, details)
	if storeOp.Amount == nil || *storeOp.Amount != "5000000" {
		t.Errorf("enriched Amount = %v, want 5000000", storeOp.Amount)
	}
	if storeOp.AssetCode == nil || *storeOp.AssetCode != "USDC" {
		t.Errorf("enriched AssetCode = %v, want USDC", storeOp.AssetCode)
	}
	if storeOp.AssetIssuer == nil || *storeOp.AssetIssuer != sponsoredAddr {
		t.Errorf("enriched AssetIssuer = %v, want %v", storeOp.AssetIssuer, sponsoredAddr)
	}
}

func TestExtractOperationDetails_ClaimClaimableBalance(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeClaimClaimableBalance, xdr.ClaimClaimableBalanceOp{
		BalanceId: testBalanceId(t),
	})

	details := extractOperationDetails(op)

	if got := details["type"]; got != "claim_claimable_balance" {
		t.Errorf("details[\"type\"] = %v, want claim_claimable_balance", got)
	}
	wantId := "00000000" + strings.Repeat("ab", 32)
	if got := details["balance_id"]; got != wantId {
		t.Errorf("details[\"balance_id\"] = %v, want %v", got, wantId)
	}

	storeOp := &store.Operation{}
	enrichOperation(storeOp, op, details)
	if storeOp.Amount != nil {
		t.Errorf("enriched Amount = %v, want nil (no promoted columns for this type)", storeOp.Amount)
	}
}

func TestExtractOperationDetails_BeginSponsoringFutureReserves(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeBeginSponsoringFutureReserves, xdr.BeginSponsoringFutureReservesOp{
		SponsoredId: xdr.MustAddress(sponsoredAddr),
	})

	details := extractOperationDetails(op)

	if got := details["type"]; got != "begin_sponsoring_future_reserves" {
		t.Errorf("details[\"type\"] = %v, want begin_sponsoring_future_reserves", got)
	}
	if got := details["sponsored_id"]; got != sponsoredAddr {
		t.Errorf("details[\"sponsored_id\"] = %v, want %v", got, sponsoredAddr)
	}
}

func TestExtractOperationDetails_EndSponsoringFutureReserves(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeEndSponsoringFutureReserves, nil)

	details := extractOperationDetails(op)

	if got := details["type"]; got != "end_sponsoring_future_reserves" {
		t.Errorf("details[\"type\"] = %v, want end_sponsoring_future_reserves", got)
	}
	if len(details) != 1 {
		t.Errorf("details has %d entries (%v), want only the type entry", len(details), details)
	}
}

func TestExtractOperationDetails_RevokeSponsorship_Account(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeRevokeSponsorship, xdr.RevokeSponsorshipOp{
		Type: xdr.RevokeSponsorshipTypeRevokeSponsorshipLedgerEntry,
		LedgerKey: &xdr.LedgerKey{
			Type: xdr.LedgerEntryTypeAccount,
			Account: &xdr.LedgerKeyAccount{
				AccountId: xdr.MustAddress(sponsoredAddr),
			},
		},
	})

	details := extractOperationDetails(op)

	want := map[string]interface{}{
		"type":             "revoke_sponsorship",
		"sponsorship_type": "account",
		"account_id":       sponsoredAddr,
	}
	for k, v := range want {
		if got := details[k]; got != v {
			t.Errorf("details[%q] = %v, want %v", k, got, v)
		}
	}
}

func TestExtractOperationDetails_RevokeSponsorship_ClaimableBalance(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeRevokeSponsorship, xdr.RevokeSponsorshipOp{
		Type: xdr.RevokeSponsorshipTypeRevokeSponsorshipLedgerEntry,
		LedgerKey: &xdr.LedgerKey{
			Type: xdr.LedgerEntryTypeClaimableBalance,
			ClaimableBalance: &xdr.LedgerKeyClaimableBalance{
				BalanceId: testBalanceId(t),
			},
		},
	})

	details := extractOperationDetails(op)

	want := map[string]interface{}{
		"type":                 "revoke_sponsorship",
		"sponsorship_type":     "claimable_balance",
		"claimable_balance_id": "00000000" + strings.Repeat("ab", 32),
	}
	for k, v := range want {
		if got := details[k]; got != v {
			t.Errorf("details[%q] = %v, want %v", k, got, v)
		}
	}
}

func TestExtractOperationDetails_RevokeSponsorship_Signer(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeRevokeSponsorship, xdr.RevokeSponsorshipOp{
		Type: xdr.RevokeSponsorshipTypeRevokeSponsorshipSigner,
		Signer: &xdr.RevokeSponsorshipOpSigner{
			AccountId: xdr.MustAddress(sponsoredAddr),
			SignerKey: xdr.MustSigner(claimantAddr),
		},
	})

	details := extractOperationDetails(op)

	want := map[string]interface{}{
		"type":              "revoke_sponsorship",
		"sponsorship_type":  "signer",
		"signer_account_id": sponsoredAddr,
		"signer_key":        claimantAddr,
	}
	for k, v := range want {
		if got := details[k]; got != v {
			t.Errorf("details[%q] = %v, want %v", k, got, v)
		}
	}
}
