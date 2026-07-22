package transform

import (
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
)

func testPoolId(t *testing.T) xdr.PoolId {
	t.Helper()
	var hash xdr.Hash
	for i := range hash {
		hash[i] = 0xcd
	}
	return xdr.PoolId(hash)
}

func TestExtractOperationDetails_LiquidityPoolDeposit(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeLiquidityPoolDeposit, xdr.LiquidityPoolDepositOp{
		LiquidityPoolId: testPoolId(t),
		MaxAmountA:      1000000000,
		MaxAmountB:      2000000000,
		MinPrice:        xdr.Price{N: 1, D: 2},
		MaxPrice:        xdr.Price{N: 2, D: 1},
	})

	details := extractOperationDetails(op)

	want := map[string]interface{}{
		"type":              "liquidity_pool_deposit",
		"liquidity_pool_id": strings.Repeat("cd", 32),
		"max_amount_a":      "1000000000",
		"max_amount_b":      "2000000000",
		"min_price":         "1/2",
		"max_price":         "2/1",
	}
	for k, v := range want {
		if got := details[k]; got != v {
			t.Errorf("details[%q] = %v, want %v", k, got, v)
		}
	}

	storeOp := &store.Operation{}
	enrichOperation(storeOp, op, details)
	if storeOp.Amount != nil {
		t.Errorf("enriched Amount = %v, want nil (deposit has two max amounts, none promoted)", storeOp.Amount)
	}
}

func TestExtractOperationDetails_LiquidityPoolWithdraw(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeLiquidityPoolWithdraw, xdr.LiquidityPoolWithdrawOp{
		LiquidityPoolId: testPoolId(t),
		Amount:          500000000,
		MinAmountA:      100000000,
		MinAmountB:      200000000,
	})

	details := extractOperationDetails(op)

	want := map[string]interface{}{
		"type":              "liquidity_pool_withdraw",
		"liquidity_pool_id": strings.Repeat("cd", 32),
		"amount":            "500000000",
		"min_amount_a":      "100000000",
		"min_amount_b":      "200000000",
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
}

func TestExtractOperationDetails_ExtendFootprintTtl(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeExtendFootprintTtl, xdr.ExtendFootprintTtlOp{
		Ext:      xdr.ExtensionPoint{V: 0},
		ExtendTo: 100000,
	})

	details := extractOperationDetails(op)

	want := map[string]interface{}{
		"type":      "extend_footprint_ttl",
		"extend_to": "100000",
	}
	for k, v := range want {
		if got := details[k]; got != v {
			t.Errorf("details[%q] = %v, want %v", k, got, v)
		}
	}
}

func TestExtractOperationDetails_RestoreFootprint(t *testing.T) {
	op := decodedOperation(t, xdr.OperationTypeRestoreFootprint, xdr.RestoreFootprintOp{
		Ext: xdr.ExtensionPoint{V: 0},
	})

	details := extractOperationDetails(op)

	if got := details["type"]; got != "restore_footprint" {
		t.Errorf("details[\"type\"] = %v, want restore_footprint", got)
	}
	if len(details) != 1 {
		t.Errorf("details has %d entries (%v), want only the type entry", len(details), details)
	}
}
