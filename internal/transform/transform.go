package transform

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/source"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
)

// LedgerFromRPC converts an RPC LedgerEntry into a store.Ledger.
func LedgerFromRPC(entry source.LedgerEntry) (*store.Ledger, error) {
	var headerEntry xdr.LedgerHeaderHistoryEntry
	if err := xdr.SafeUnmarshalBase64(entry.HeaderXDR, &headerEntry); err != nil {
		return nil, fmt.Errorf("unmarshal ledger header: %w", err)
	}
	header := headerEntry.Header

	closedAt, err := parseLedgerCloseTime(entry.LedgerCloseTime)
	if err != nil {
		return nil, fmt.Errorf("parse ledger close time: %w", err)
	}

	headerXDR := entry.HeaderXDR
	return &store.Ledger{
		Sequence:        uint32(header.LedgerSeq),
		Hash:            entry.Hash,
		PrevHash:        hex.EncodeToString(header.PreviousLedgerHash[:]),
		ClosedAt:        closedAt,
		TotalCoins:      int64(header.TotalCoins),
		FeePool:         int64(header.FeePool),
		BaseFee:         int32(header.BaseFee),
		BaseReserve:     int32(header.BaseReserve),
		MaxTxSetSize:    int32(header.MaxTxSetSize),
		ProtocolVersion: int32(header.LedgerVersion),
		HeaderXDR:       &headerXDR,
	}, nil
}

// TransactionFromRPC converts an RPC TransactionEntry into a store.Transaction.
func TransactionFromRPC(entry source.TransactionEntry, networkPassphrase string) (*store.Transaction, error) {
	var envelope xdr.TransactionEnvelope
	if err := xdr.SafeUnmarshalBase64(entry.EnvelopeXDR, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}

	var result xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(entry.ResultXDR, &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	txHash, err := computeTransactionHash(envelope, networkPassphrase)
	if err != nil {
		return nil, fmt.Errorf("compute tx hash: %w", err)
	}

	sourceAccount := envelope.SourceAccount()
	// Always use the base G-address (ToAccountId strips the muxed memo ID).
	// The full M-address is stored separately in account_muxed when present.
	accountAddr := sourceAccount.ToAccountId().Address()

	memo := envelope.Memo()
	memoType := int16(memo.Type)
	var memoText *string
	var memoHash *string
	switch memo.Type {
	case xdr.MemoTypeMemoText:
		text := memo.MustText()
		memoText = &text
	case xdr.MemoTypeMemoHash:
		h := memo.MustHash()
		hashStr := hex.EncodeToString(h[:])
		memoHash = &hashStr
	case xdr.MemoTypeMemoReturn:
		h := memo.MustRetHash()
		hashStr := hex.EncodeToString(h[:])
		memoHash = &hashStr
	case xdr.MemoTypeMemoId:
		id := memo.MustId()
		idStr := fmt.Sprintf("%d", id)
		memoText = &idStr
	}

	// Determine muxed account fields if applicable
	_, accountMuxed, accountMuxedID := parseMuxedAccount(sourceAccount)

	// Status: 1 = success, 0 = failed
	var status int16
	resultCode := result.Result.Code
	if resultCode == xdr.TransactionResultCodeTxSuccess || resultCode == xdr.TransactionResultCodeTxFeeBumpInnerSuccess {
		status = 1
	}

	// Detect Soroban transactions
	isSoroban := hasSorobanOp(envelope.Operations())

	resultMetaXDR := entry.ResultMetaXDR
	createdAt := time.Unix(entry.CreatedAt, 0).UTC()

	return &store.Transaction{
		Hash:             txHash,
		LedgerSequence:   entry.Ledger,
		ApplicationOrder: entry.ApplicationOrder,
		Account:          accountAddr,
		AccountMuxed:     accountMuxed,
		AccountMuxedID:   accountMuxedID,
		AccountSequence:  envelope.SeqNum(),
		FeeCharged:       int64(result.FeeCharged),
		MaxFee:           int64(envelope.Fee()),
		OperationCount:   int32(envelope.OperationsCount()),
		MemoType:         memoType,
		MemoText:         memoText,
		MemoHash:         memoHash,
		Status:           status,
		IsSoroban:        isSoroban,
		EnvelopeXDR:      entry.EnvelopeXDR,
		ResultXDR:        entry.ResultXDR,
		ResultMetaXDR:    &resultMetaXDR,
		CreatedAt:        createdAt,
	}, nil
}

// OperationsFromRPC extracts operations from a transaction entry.
func OperationsFromRPC(entry source.TransactionEntry, networkPassphrase string) ([]store.Operation, error) {
	var envelope xdr.TransactionEnvelope
	if err := xdr.SafeUnmarshalBase64(entry.EnvelopeXDR, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}

	txHash, err := computeTransactionHash(envelope, networkPassphrase)
	if err != nil {
		return nil, fmt.Errorf("compute tx hash: %w", err)
	}

	ops := envelope.Operations()
	createdAt := time.Unix(entry.CreatedAt, 0).UTC()
	result := make([]store.Operation, 0, len(ops))

	for i, op := range ops {
		opType := op.Body.Type
		typeName := operationTypeName(opType)

		var sourceAccount *string
		var sourceAccountMuxed *string
		var sourceMuxedID *int64
		if op.SourceAccount != nil {
			base, muxed, muxedID := parseMuxedAccount(*op.SourceAccount)
			sourceAccount = &base
			sourceAccountMuxed = muxed
			sourceMuxedID = muxedID
		}

		details := extractOperationDetails(op)
		detailsJSON, _ := json.Marshal(details)

		storeOp := store.Operation{
			TransactionHash:    txHash,
			ApplicationOrder:   int32(i + 1),
			Type:               int16(opType),
			TypeName:           typeName,
			SourceAccount:      sourceAccount,
			SourceAccountMuxed: sourceAccountMuxed,
			SourceMuxedID:      sourceMuxedID,
			Details:            string(detailsJSON),
			CreatedAt:          createdAt,
		}

		// Extract denormalized fields from specific operation types
		enrichOperation(&storeOp, op, details)

		result = append(result, storeOp)
	}

	return result, nil
}

func computeTransactionHash(envelope xdr.TransactionEnvelope, networkPassphrase string) (string, error) {
	hash, err := network.HashTransactionInEnvelope(envelope, networkPassphrase)
	if err != nil {
		return "", fmt.Errorf("hash transaction: %w", err)
	}
	return hex.EncodeToString(hash[:]), nil
}

func parseLedgerCloseTime(s string) (time.Time, error) {
	// Try unix timestamp (integer as string)
	var ts int64
	if _, err := fmt.Sscanf(s, "%d", &ts); err == nil {
		return time.Unix(ts, 0).UTC(), nil
	}

	// Try RFC3339
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("cannot parse %q as timestamp", s)
	}
	return t.UTC(), nil
}

func hasSorobanOp(ops []xdr.Operation) bool {
	for _, op := range ops {
		switch op.Body.Type {
		case xdr.OperationTypeInvokeHostFunction,
			xdr.OperationTypeExtendFootprintTtl,
			xdr.OperationTypeRestoreFootprint:
			return true
		}
	}
	return false
}

func operationTypeName(t xdr.OperationType) string {
	s := t.String()
	// Convert from "OperationTypePayment" to "payment"
	s = strings.TrimPrefix(s, "OperationType")
	if s == "" {
		return fmt.Sprintf("unknown_%d", int32(t))
	}
	// Convert CamelCase to snake_case
	return camelToSnake(s)
}

func camelToSnake(s string) string {
	var result []byte
	for i, c := range s {
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				result = append(result, '_')
			}
			result = append(result, byte(c+'a'-'A'))
		} else {
			result = append(result, byte(c))
		}
	}
	return string(result)
}

func extractOperationDetails(op xdr.Operation) map[string]interface{} {
	details := map[string]interface{}{
		"type": operationTypeName(op.Body.Type),
	}

	switch op.Body.Type {
	case xdr.OperationTypePayment:
		payment := op.Body.MustPaymentOp()
		details["destination"] = payment.Destination.Address()
		details["amount"] = fmt.Sprintf("%d", payment.Amount)
		details["asset"] = assetString(payment.Asset)
	case xdr.OperationTypeCreateAccount:
		create := op.Body.MustCreateAccountOp()
		details["destination"] = create.Destination.Address()
		details["starting_balance"] = fmt.Sprintf("%d", create.StartingBalance)
	case xdr.OperationTypePathPaymentStrictReceive:
		pp := op.Body.MustPathPaymentStrictReceiveOp()
		details["destination"] = pp.Destination.Address()
		details["dest_amount"] = fmt.Sprintf("%d", pp.DestAmount)
		details["dest_asset"] = assetString(pp.DestAsset)
		details["send_asset"] = assetString(pp.SendAsset)
		details["send_max"] = fmt.Sprintf("%d", pp.SendMax)
	case xdr.OperationTypePathPaymentStrictSend:
		pp := op.Body.MustPathPaymentStrictSendOp()
		details["destination"] = pp.Destination.Address()
		details["send_amount"] = fmt.Sprintf("%d", pp.SendAmount)
		details["send_asset"] = assetString(pp.SendAsset)
		details["dest_asset"] = assetString(pp.DestAsset)
		details["dest_min"] = fmt.Sprintf("%d", pp.DestMin)
	case xdr.OperationTypeInvokeHostFunction:
		invoke := op.Body.MustInvokeHostFunctionOp()
		details["function_type"] = invoke.HostFunction.Type.String()
	case xdr.OperationTypeChangeTrust:
		ct := op.Body.MustChangeTrustOp()
		details["limit"] = fmt.Sprintf("%d", ct.Limit)
	case xdr.OperationTypeManageSellOffer:
		o := op.Body.MustManageSellOfferOp()
		details["selling"] = assetString(o.Selling)
		details["buying"] = assetString(o.Buying)
		details["amount"] = fmt.Sprintf("%d", o.Amount)
		details["price"] = fmt.Sprintf("%d/%d", o.Price.N, o.Price.D)
		details["offer_id"] = fmt.Sprintf("%d", o.OfferId)
	case xdr.OperationTypeManageBuyOffer:
		o := op.Body.MustManageBuyOfferOp()
		details["selling"] = assetString(o.Selling)
		details["buying"] = assetString(o.Buying)
		details["buy_amount"] = fmt.Sprintf("%d", o.BuyAmount)
		details["price"] = fmt.Sprintf("%d/%d", o.Price.N, o.Price.D)
		details["offer_id"] = fmt.Sprintf("%d", o.OfferId)
	case xdr.OperationTypeCreatePassiveSellOffer:
		o := op.Body.MustCreatePassiveSellOfferOp()
		details["selling"] = assetString(o.Selling)
		details["buying"] = assetString(o.Buying)
		details["amount"] = fmt.Sprintf("%d", o.Amount)
		details["price"] = fmt.Sprintf("%d/%d", o.Price.N, o.Price.D)
	case xdr.OperationTypeCreateClaimableBalance:
		o := op.Body.MustCreateClaimableBalanceOp()
		details["asset"] = assetString(o.Asset)
		details["amount"] = fmt.Sprintf("%d", o.Amount)
		claimants := make([]map[string]interface{}, 0, len(o.Claimants))
		for _, c := range o.Claimants {
			v0 := c.MustV0()
			claimants = append(claimants, map[string]interface{}{
				"destination": v0.Destination.Address(),
				"predicate":   claimPredicateMap(v0.Predicate),
			})
		}
		details["claimants"] = claimants
	case xdr.OperationTypeClaimClaimableBalance:
		o := op.Body.MustClaimClaimableBalanceOp()
		details["balance_id"] = claimableBalanceIdString(o.BalanceId)
	case xdr.OperationTypeBeginSponsoringFutureReserves:
		o := op.Body.MustBeginSponsoringFutureReservesOp()
		details["sponsored_id"] = o.SponsoredId.Address()
	case xdr.OperationTypeEndSponsoringFutureReserves:
		// end_sponsoring_future_reserves has an empty operation body; the
		// "type" entry set above is its complete detail.
	case xdr.OperationTypeRevokeSponsorship:
		o := op.Body.MustRevokeSponsorshipOp()
		revokeSponsorshipDetails(details, o)
	case xdr.OperationTypeLiquidityPoolDeposit:
		o := op.Body.MustLiquidityPoolDepositOp()
		poolId := o.LiquidityPoolId
		details["liquidity_pool_id"] = hex.EncodeToString(poolId[:])
		details["max_amount_a"] = fmt.Sprintf("%d", o.MaxAmountA)
		details["max_amount_b"] = fmt.Sprintf("%d", o.MaxAmountB)
		details["min_price"] = fmt.Sprintf("%d/%d", o.MinPrice.N, o.MinPrice.D)
		details["max_price"] = fmt.Sprintf("%d/%d", o.MaxPrice.N, o.MaxPrice.D)
	case xdr.OperationTypeLiquidityPoolWithdraw:
		o := op.Body.MustLiquidityPoolWithdrawOp()
		poolId := o.LiquidityPoolId
		details["liquidity_pool_id"] = hex.EncodeToString(poolId[:])
		details["amount"] = fmt.Sprintf("%d", o.Amount)
		details["min_amount_a"] = fmt.Sprintf("%d", o.MinAmountA)
		details["min_amount_b"] = fmt.Sprintf("%d", o.MinAmountB)
	case xdr.OperationTypeExtendFootprintTtl:
		o := op.Body.MustExtendFootprintTtlOp()
		details["extend_to"] = fmt.Sprintf("%d", o.ExtendTo)
	case xdr.OperationTypeRestoreFootprint:
		// The footprint being restored comes from the transaction's Soroban
		// data, not the operation body; the "type" entry set above is the
		// complete detail.
	}

	return details
}

func enrichOperation(storeOp *store.Operation, op xdr.Operation, details map[string]interface{}) {
	switch op.Body.Type {
	case xdr.OperationTypePayment:
		payment := op.Body.MustPaymentOp()
		base, muxed, muxedID := parseMuxedAccount(payment.Destination)
		storeOp.Destination = &base
		storeOp.DestinationMuxed = muxed
		storeOp.DestinationMuxedID = muxedID
		amount := fmt.Sprintf("%d", payment.Amount)
		storeOp.Amount = &amount
		code, issuer := assetParts(payment.Asset)
		storeOp.AssetCode = code
		storeOp.AssetIssuer = issuer
	case xdr.OperationTypeCreateAccount:
		create := op.Body.MustCreateAccountOp()
		dest := create.Destination.Address()
		storeOp.Destination = &dest
		amount := fmt.Sprintf("%d", create.StartingBalance)
		storeOp.Amount = &amount
	case xdr.OperationTypePathPaymentStrictReceive:
		pp := op.Body.MustPathPaymentStrictReceiveOp()
		base, muxed, muxedID := parseMuxedAccount(pp.Destination)
		storeOp.Destination = &base
		storeOp.DestinationMuxed = muxed
		storeOp.DestinationMuxedID = muxedID
		amount := fmt.Sprintf("%d", pp.DestAmount)
		storeOp.Amount = &amount
		code, issuer := assetParts(pp.DestAsset)
		storeOp.AssetCode = code
		storeOp.AssetIssuer = issuer
	case xdr.OperationTypePathPaymentStrictSend:
		pp := op.Body.MustPathPaymentStrictSendOp()
		base, muxed, muxedID := parseMuxedAccount(pp.Destination)
		storeOp.Destination = &base
		storeOp.DestinationMuxed = muxed
		storeOp.DestinationMuxedID = muxedID
		amount := fmt.Sprintf("%d", pp.SendAmount)
		storeOp.Amount = &amount
		code, issuer := assetParts(pp.SendAsset)
		storeOp.AssetCode = code
		storeOp.AssetIssuer = issuer
	case xdr.OperationTypeInvokeHostFunction:
		invoke := op.Body.MustInvokeHostFunctionOp()
		fnType := invoke.HostFunction.Type.String()
		storeOp.FunctionName = &fnType
	case xdr.OperationTypeManageSellOffer:
		o := op.Body.MustManageSellOfferOp()
		amount := fmt.Sprintf("%d", o.Amount)
		storeOp.Amount = &amount
		code, issuer := assetParts(o.Selling)
		storeOp.AssetCode = code
		storeOp.AssetIssuer = issuer
	case xdr.OperationTypeManageBuyOffer:
		o := op.Body.MustManageBuyOfferOp()
		amount := fmt.Sprintf("%d", o.BuyAmount)
		storeOp.Amount = &amount
		code, issuer := assetParts(o.Selling)
		storeOp.AssetCode = code
		storeOp.AssetIssuer = issuer
	case xdr.OperationTypeCreatePassiveSellOffer:
		o := op.Body.MustCreatePassiveSellOfferOp()
		amount := fmt.Sprintf("%d", o.Amount)
		storeOp.Amount = &amount
		code, issuer := assetParts(o.Selling)
		storeOp.AssetCode = code
		storeOp.AssetIssuer = issuer
	case xdr.OperationTypeCreateClaimableBalance:
		o := op.Body.MustCreateClaimableBalanceOp()
		amount := fmt.Sprintf("%d", o.Amount)
		storeOp.Amount = &amount
		code, issuer := assetParts(o.Asset)
		storeOp.AssetCode = code
		storeOp.AssetIssuer = issuer
	case xdr.OperationTypeLiquidityPoolWithdraw:
		o := op.Body.MustLiquidityPoolWithdrawOp()
		amount := fmt.Sprintf("%d", o.Amount)
		storeOp.Amount = &amount
	}
}

// parseMuxedAccount splits a MuxedAccount into:
//   - base: the plain 56-char G-address (always present)
//   - muxed: the full M-address (only when muxed, nil otherwise)
//   - muxedID: the 64-bit integer muxed ID (only when muxed, nil otherwise)
func parseMuxedAccount(m xdr.MuxedAccount) (base string, muxed *string, muxedID *int64) {
	base = m.ToAccountId().Address()
	if m.Type == xdr.CryptoKeyTypeKeyTypeMuxedEd25519 {
		addr := m.Address()
		muxed = &addr
		if rawID, ok := m.GetMed25519(); ok {
			id64 := int64(rawID.Id)
			muxedID = &id64
		}
	}
	return
}

func assetString(asset xdr.Asset) string {
	switch asset.Type {
	case xdr.AssetTypeAssetTypeNative:
		return "native"
	case xdr.AssetTypeAssetTypeCreditAlphanum4:
		a4 := asset.MustAlphaNum4()
		return fmt.Sprintf("%s:%s", strings.TrimRight(string(a4.AssetCode[:]), "\x00"), a4.Issuer.Address())
	case xdr.AssetTypeAssetTypeCreditAlphanum12:
		a12 := asset.MustAlphaNum12()
		return fmt.Sprintf("%s:%s", strings.TrimRight(string(a12.AssetCode[:]), "\x00"), a12.Issuer.Address())
	default:
		return "unknown"
	}
}

func assetParts(asset xdr.Asset) (*string, *string) {
	switch asset.Type {
	case xdr.AssetTypeAssetTypeNative:
		code := "XLM"
		return &code, nil
	case xdr.AssetTypeAssetTypeCreditAlphanum4:
		a4 := asset.MustAlphaNum4()
		code := strings.TrimRight(string(a4.AssetCode[:]), "\x00")
		issuer := a4.Issuer.Address()
		return &code, &issuer
	case xdr.AssetTypeAssetTypeCreditAlphanum12:
		a12 := asset.MustAlphaNum12()
		code := strings.TrimRight(string(a12.AssetCode[:]), "\x00")
		issuer := a12.Issuer.Address()
		return &code, &issuer
	default:
		return nil, nil
	}
}

// claimPredicateMap converts a ClaimPredicate into a JSON-friendly map using
// the same field names Horizon uses for claimable balance predicates
// (unconditional, and, or, not, abs_before, rel_before).
func claimPredicateMap(p xdr.ClaimPredicate) map[string]interface{} {
	switch p.Type {
	case xdr.ClaimPredicateTypeClaimPredicateUnconditional:
		return map[string]interface{}{"unconditional": true}
	case xdr.ClaimPredicateTypeClaimPredicateAnd:
		preds := p.MustAndPredicates()
		sub := make([]map[string]interface{}, 0, len(preds))
		for _, sp := range preds {
			sub = append(sub, claimPredicateMap(sp))
		}
		return map[string]interface{}{"and": sub}
	case xdr.ClaimPredicateTypeClaimPredicateOr:
		preds := p.MustOrPredicates()
		sub := make([]map[string]interface{}, 0, len(preds))
		for _, sp := range preds {
			sub = append(sub, claimPredicateMap(sp))
		}
		return map[string]interface{}{"or": sub}
	case xdr.ClaimPredicateTypeClaimPredicateNot:
		// The not-arm is optional in XDR and may be null.
		if inner := p.MustNotPredicate(); inner != nil {
			return map[string]interface{}{"not": claimPredicateMap(*inner)}
		}
		return map[string]interface{}{"not": nil}
	case xdr.ClaimPredicateTypeClaimPredicateBeforeAbsoluteTime:
		return map[string]interface{}{"abs_before": fmt.Sprintf("%d", p.MustAbsBefore())}
	case xdr.ClaimPredicateTypeClaimPredicateBeforeRelativeTime:
		return map[string]interface{}{"rel_before": fmt.Sprintf("%d", p.MustRelBefore())}
	default:
		return map[string]interface{}{"unknown": p.Type.String()}
	}
}

// claimableBalanceIdString renders a claimable balance ID as the
// type-prefixed hex string used by Horizon and Stellar RPC
// (e.g. "00000000d1d73327fc560cbb3f5a9de85e5fdfebb5c4525c47ecc515989c8a7746a94b8a").
func claimableBalanceIdString(id xdr.ClaimableBalanceId) string {
	// MarshalHex cannot realistically fail here: the ID was already decoded
	// from valid XDR, and re-encoding a decoded value is infallible. The
	// empty-string branch exists only to satisfy the error contract.
	s, err := xdr.MarshalHex(id)
	if err != nil {
		return ""
	}
	return s
}

// revokeSponsorshipDetails fills details for a revoke_sponsorship operation:
// the kind of sponsorship being revoked plus the ledger key or signer it
// targets, using Horizon's field names.
func revokeSponsorshipDetails(details map[string]interface{}, o xdr.RevokeSponsorshipOp) {
	switch o.Type {
	case xdr.RevokeSponsorshipTypeRevokeSponsorshipLedgerEntry:
		key := o.MustLedgerKey()
		switch key.Type {
		case xdr.LedgerEntryTypeAccount:
			details["sponsorship_type"] = "account"
			details["account_id"] = key.Account.AccountId.Address()
		case xdr.LedgerEntryTypeTrustline:
			details["sponsorship_type"] = "trustline"
			details["trustline_account_id"] = key.TrustLine.AccountId.Address()
			details["trustline_asset"] = trustLineAssetString(key.TrustLine.Asset)
		case xdr.LedgerEntryTypeOffer:
			details["sponsorship_type"] = "offer"
			details["seller_id"] = key.Offer.SellerId.Address()
			details["offer_id"] = fmt.Sprintf("%d", key.Offer.OfferId)
		case xdr.LedgerEntryTypeData:
			details["sponsorship_type"] = "data"
			details["data_account_id"] = key.Data.AccountId.Address()
			details["data_name"] = string(key.Data.DataName)
		case xdr.LedgerEntryTypeClaimableBalance:
			details["sponsorship_type"] = "claimable_balance"
			// Type-prefixed hex (via MarshalHex), the balance ID format
			// Horizon and Stellar RPC accept; liquidity_pool_id below is
			// plain hex because pool IDs have no type prefix anywhere else
			// in the schema (liquidity_pools/trades store bare 64-char hex).
			details["claimable_balance_id"] = claimableBalanceIdString(key.ClaimableBalance.BalanceId)
		case xdr.LedgerEntryTypeLiquidityPool:
			details["sponsorship_type"] = "liquidity_pool"
			poolId := key.LiquidityPool.LiquidityPoolId
			details["liquidity_pool_id"] = hex.EncodeToString(poolId[:])
		default:
			// All revocable sponsorship targets are covered above; record
			// anything unexpected instead of dropping it silently.
			details["sponsorship_type"] = "unknown"
			details["ledger_key_type"] = key.Type.String()
		}
	case xdr.RevokeSponsorshipTypeRevokeSponsorshipSigner:
		s := o.MustSigner()
		details["sponsorship_type"] = "signer"
		details["signer_account_id"] = s.AccountId.Address()
		details["signer_key"] = s.SignerKey.Address()
	}
}

// trustLineAssetString renders a trustline asset, which unlike a regular
// asset may also be a liquidity pool share.
func trustLineAssetString(a xdr.TrustLineAsset) string {
	if a.Type == xdr.AssetTypeAssetTypePoolShare {
		id := a.MustLiquidityPoolId()
		return "liquidity_pool:" + hex.EncodeToString(id[:])
	}
	return assetString(a.ToAsset())
}
