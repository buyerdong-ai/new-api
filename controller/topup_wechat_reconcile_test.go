package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyWeChatBillDigest(t *testing.T) {
	bill := []byte("trade bill")
	digest := sha256.Sum256(bill)
	require.NoError(t, verifyWeChatBillDigest(bill, "SHA256", hex.EncodeToString(digest[:])))
	require.Error(t, verifyWeChatBillDigest(bill, "SHA256", "incorrect"))
	require.Error(t, verifyWeChatBillDigest(bill, "MD5", "incorrect"))
}

func TestParseWeChatTradeBill(t *testing.T) {
	bill := "交易时间,微信支付订单号,商户订单号,交易状态,总金额\n" +
		"2026-08-15 10:00:00,`wx-1,`local-1,`SUCCESS,`14.60\n"
	entries, err := parseWeChatTradeBill([]byte(bill))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "wx-1", entries[0].TransactionID)
	assert.Equal(t, "local-1", entries[0].TradeNo)
	assert.Equal(t, int64(1460), entries[0].AmountFen)

	_, err = parseWeChatTradeBill([]byte("微信支付订单号,商户订单号,交易状态,总金额\nwx-1,local-1,SUCCESS,14.601\n"))
	require.Error(t, err)
	_, err = parseWeChatTradeBill([]byte("unrelated,column\nvalue,value\n"))
	require.Error(t, err)
}

func TestReconcileWeChatTradeBillDifferences(t *testing.T) {
	matchingTransaction := "wx-match"
	wrongTransaction := "wx-local-wrong"
	entries := []weChatTradeBillEntry{
		{TradeNo: "match", TransactionID: matchingTransaction, State: "SUCCESS", AmountFen: 100},
		{TradeNo: "unknown-local", TransactionID: "wx-unknown", State: "SUCCESS", AmountFen: 200},
		{TradeNo: "mismatch", TransactionID: "wx-provider", State: "SUCCESS", AmountFen: 300},
	}
	local := []model.TopUp{
		{TradeNo: "match", ProviderTradeNo: &matchingTransaction, ProviderAmount: 100, ProviderCurrency: "CNY"},
		{TradeNo: "missing-provider", ProviderTradeNo: &matchingTransaction, ProviderAmount: 100, ProviderCurrency: "CNY"},
		{TradeNo: "mismatch", ProviderTradeNo: &wrongTransaction, ProviderAmount: 301, ProviderCurrency: "CNY"},
	}

	result, err := reconcileWeChatTradeBill(entries, local)
	require.NoError(t, err)
	assert.Equal(t, int64(3), result.ProviderSuccessCount)
	assert.Equal(t, int64(3), result.LocalSuccessCount)
	assert.Equal(t, int64(1), result.UnknownOrderCount)
	assert.Equal(t, int64(1), result.MissingOrderCount)
	assert.Equal(t, int64(1), result.AmountMismatchCount)
	assert.Equal(t, int64(1), result.TransactionMismatchCount)
	assert.Contains(t, result.DifferenceSample, "unknown-local")
	assert.Contains(t, result.DifferenceSample, "missing-provider")
}

func TestReconcileWeChatTradeBillLimitsDifferenceSamples(t *testing.T) {
	entries := make([]weChatTradeBillEntry, maxReconciliationSamples+5)
	for index := range entries {
		entries[index] = weChatTradeBillEntry{
			TradeNo:       fmt.Sprintf("unknown-%d", index),
			TransactionID: fmt.Sprintf("wx-%d", index),
			State:         "SUCCESS",
			AmountFen:     100,
		}
	}

	result, err := reconcileWeChatTradeBill(entries, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(maxReconciliationSamples+5), result.UnknownOrderCount)
	var samples []weChatReconciliationDifference
	require.NoError(t, common.UnmarshalJsonStr(result.DifferenceSample, &samples))
	assert.Len(t, samples, maxReconciliationSamples)
}