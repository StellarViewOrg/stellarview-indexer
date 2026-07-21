package transform

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
)

// decodedOperation builds an xdr.Operation for opType/body, round-trips it
// through base64 XDR marshal/unmarshal (as OperationsFromRPC does for
// operations coming off the network), and returns the decoded result.
func decodedOperation(t *testing.T, opType xdr.OperationType, body interface{}) xdr.Operation {
	t.Helper()

	opBody, err := xdr.NewOperationBody(opType, body)
	if err != nil {
		t.Fatalf("NewOperationBody(%s) failed: %v", opType, err)
	}
	op := xdr.Operation{Body: opBody}

	encoded, err := xdr.MarshalBase64(op)
	if err != nil {
		t.Fatalf("MarshalBase64 failed: %v", err)
	}

	var decoded xdr.Operation
	if err := xdr.SafeUnmarshalBase64(encoded, &decoded); err != nil {
		t.Fatalf("SafeUnmarshalBase64 failed: %v", err)
	}
	return decoded
}

func usdcIssuedAsset(t *testing.T) xdr.Asset {
	t.Helper()
	return creditXDRAsset("USDC", "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN")
}

func TestExtractOperationDetails_ManageSellOffer(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeManageSellOffer, xdr.ManageSellOfferOp{
		Selling: nativeXDRAsset(),
		Buying:  usdcIssuedAsset(t),
		Amount:  1000000000,
		Price:   xdr.Price{N: 12, D: 100},
		OfferId: 42,
	})

	details := extractOperationDetails(op)

	want := map[string]interface{}{
		"type":     "manage_sell_offer",
		"selling":  "native",
		"buying":   "USDC:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN",
		"amount":   "1000000000",
		"price":    "12/100",
		"offer_id": "42",
	}
	for k, v := range want {
		if got := details[k]; got != v {
			t.Errorf("details[%q] = %v, want %v", k, got, v)
		}
	}

	storeOp := &store.Operation{}
	enrichOperation(storeOp, op, details)
	if storeOp.Amount == nil || *storeOp.Amount != "1000000000" {
		t.Errorf("enriched Amount = %v, want 1000000000", storeOp.Amount)
	}
	if storeOp.AssetCode == nil || *storeOp.AssetCode != "XLM" {
		t.Errorf("enriched AssetCode = %v, want XLM (selling asset)", storeOp.AssetCode)
	}
	if storeOp.AssetIssuer != nil {
		t.Errorf("enriched AssetIssuer = %v, want nil for native selling asset", storeOp.AssetIssuer)
	}
}

func TestExtractOperationDetails_ManageBuyOffer(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeManageBuyOffer, xdr.ManageBuyOfferOp{
		Selling:   usdcIssuedAsset(t),
		Buying:    nativeXDRAsset(),
		BuyAmount: 500000000,
		Price:     xdr.Price{N: 1, D: 5},
		OfferId:   7,
	})

	details := extractOperationDetails(op)

	want := map[string]interface{}{
		"type":       "manage_buy_offer",
		"selling":    "USDC:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN",
		"buying":     "native",
		"buy_amount": "500000000",
		"price":      "1/5",
		"offer_id":   "7",
	}
	for k, v := range want {
		if got := details[k]; got != v {
			t.Errorf("details[%q] = %v, want %v", k, got, v)
		}
	}

	storeOp := &store.Operation{}
	enrichOperation(storeOp, op, details)
	if storeOp.Amount == nil || *storeOp.Amount != "500000000" {
		t.Errorf("enriched Amount = %v, want 500000000", storeOp.Amount)
	}
	if storeOp.AssetCode == nil || *storeOp.AssetCode != "USDC" {
		t.Errorf("enriched AssetCode = %v, want USDC (selling asset)", storeOp.AssetCode)
	}
	if storeOp.AssetIssuer == nil || *storeOp.AssetIssuer != "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN" {
		t.Errorf("enriched AssetIssuer = %v, want GAT3T5...", storeOp.AssetIssuer)
	}
}

func TestExtractOperationDetails_CreatePassiveSellOffer(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeCreatePassiveSellOffer, xdr.CreatePassiveSellOfferOp{
		Selling: nativeXDRAsset(),
		Buying:  usdcIssuedAsset(t),
		Amount:  250000000,
		Price:   xdr.Price{N: 3, D: 10},
	})

	details := extractOperationDetails(op)

	want := map[string]interface{}{
		"type":    "create_passive_sell_offer",
		"selling": "native",
		"buying":  "USDC:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN",
		"amount":  "250000000",
		"price":   "3/10",
	}
	for k, v := range want {
		if got := details[k]; got != v {
			t.Errorf("details[%q] = %v, want %v", k, got, v)
		}
	}
	if _, ok := details["offer_id"]; ok {
		t.Errorf("create_passive_sell_offer has no offer_id field, but details contains one: %v", details["offer_id"])
	}

	storeOp := &store.Operation{}
	enrichOperation(storeOp, op, details)
	if storeOp.Amount == nil || *storeOp.Amount != "250000000" {
		t.Errorf("enriched Amount = %v, want 250000000", storeOp.Amount)
	}
	if storeOp.AssetCode == nil || *storeOp.AssetCode != "XLM" {
		t.Errorf("enriched AssetCode = %v, want XLM (selling asset)", storeOp.AssetCode)
	}
}
