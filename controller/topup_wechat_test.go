package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
)

func TestVerifiedWeChatPaymentFromTransaction(t *testing.T) {
	originalAppID := setting.WeChatPayAppID
	originalMchID := setting.WeChatPayMchID
	setting.WeChatPayAppID = "wx-app-id"
	setting.WeChatPayMchID = "merchant-id"
	t.Cleanup(func() {
		setting.WeChatPayAppID = originalAppID
		setting.WeChatPayMchID = originalMchID
	})

	valid := func() *payments.Transaction {
		return &payments.Transaction{
			Appid:         core.String("wx-app-id"),
			Mchid:         core.String("merchant-id"),
			OutTradeNo:    core.String("local-order"),
			TransactionId: core.String("wechat-transaction"),
			TradeState:    core.String("SUCCESS"),
			Amount: &payments.TransactionAmount{
				Total:    core.Int64(100),
				Currency: core.String("CNY"),
			},
		}
	}

	payment, err := verifiedWeChatPaymentFromTransaction(valid())
	require.NoError(t, err)
	assert.Equal(t, "wechat-transaction", payment.TransactionID)

	tests := []struct {
		name   string
		mutate func(*payments.Transaction)
	}{
		{name: "appid mismatch", mutate: func(transaction *payments.Transaction) { transaction.Appid = core.String("other-app") }},
		{name: "merchant mismatch", mutate: func(transaction *payments.Transaction) { transaction.Mchid = core.String("other-merchant") }},
		{name: "missing transaction id", mutate: func(transaction *payments.Transaction) { transaction.TransactionId = nil }},
		{name: "not successful", mutate: func(transaction *payments.Transaction) { transaction.TradeState = core.String("NOTPAY") }},
		{name: "amount mismatch currency", mutate: func(transaction *payments.Transaction) { transaction.Amount.Currency = core.String("USD") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transaction := valid()
			test.mutate(transaction)
			_, err := verifiedWeChatPaymentFromTransaction(transaction)
			require.Error(t, err)
		})
	}
}

func TestValidateWeChatTransactionIdentity(t *testing.T) {
	originalAppID := setting.WeChatPayAppID
	originalMchID := setting.WeChatPayMchID
	setting.WeChatPayAppID = "wx-app-id"
	setting.WeChatPayMchID = "merchant-id"
	t.Cleanup(func() {
		setting.WeChatPayAppID = originalAppID
		setting.WeChatPayMchID = originalMchID
	})

	transaction := &payments.Transaction{
		Appid:        core.String("wx-app-id"),
		Mchid:        core.String("merchant-id"),
		OutTradeNo:   core.String("local-order"),
		TradeState:   core.String("NOTPAY"),
	}
	require.NoError(t, validateWeChatTransactionIdentity(transaction, "local-order"))
	require.Error(t, validateWeChatTransactionIdentity(transaction, "other-order"))
}

func TestValidateWeChatTopUpAmountMaximum(t *testing.T) {
	require.NoError(t, validateWeChatTopUpAmount(maxWeChatTopUp))
	require.Error(t, validateWeChatTopUpAmount(maxWeChatTopUp+1))
}