package model

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompleteWeChatTopUpRejectsInvalidPayment(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		paidFen  int64
		expected error
	}{
		{
			name:     "amount mismatch",
			provider: PaymentProviderWeChatPay,
			paidFen:  1459,
		},
		{
			name:     "provider mismatch",
			provider: PaymentProviderEpay,
			paidFen:  1460,
			expected: ErrPaymentMethodMismatch,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncateTables(t)
			user := User{Username: "wechat-invalid-" + test.name, Quota: 100}
			require.NoError(t, DB.Create(&user).Error)
			topUp := TopUp{
				UserId:          user.Id,
				Amount:          2,
				Money:           14.6,
				TradeNo:         "wechat-invalid-" + test.name,
				PaymentMethod:   PaymentProviderWeChatPay,
				PaymentProvider: test.provider,
				Status:          common.TopUpStatusPending,
			}
			require.NoError(t, DB.Create(&topUp).Error)

			err := CompleteWeChatTopUp(verifiedWeChatPayment(topUp.TradeNo, "wx-invalid-"+test.name, test.paidFen), "127.0.0.1")
			if test.expected != nil {
				require.ErrorIs(t, err, test.expected)
			} else {
				require.Error(t, err)
			}

			require.NoError(t, DB.First(&user, user.Id).Error)
			require.NoError(t, DB.First(&topUp, topUp.Id).Error)
			assert.Equal(t, 100, user.Quota)
			assert.Equal(t, common.TopUpStatusPending, topUp.Status)
		})
	}
}

func TestCompleteWeChatTopUpRequiresCompleteVerifiedPayment(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*VerifiedWeChatPayment)
	}{
		{name: "missing app id", mutate: func(payment *VerifiedWeChatPayment) { payment.AppID = "" }},
		{name: "missing merchant id", mutate: func(payment *VerifiedWeChatPayment) { payment.MchID = "" }},
		{name: "missing transaction id", mutate: func(payment *VerifiedWeChatPayment) { payment.TransactionID = "" }},
		{name: "not successful", mutate: func(payment *VerifiedWeChatPayment) { payment.TradeState = "NOTPAY" }},
		{name: "wrong currency", mutate: func(payment *VerifiedWeChatPayment) { payment.Currency = "USD" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payment := verifiedWeChatPayment("wechat-required", "wx-required", 100)
			test.mutate(&payment)
			require.Error(t, CompleteWeChatTopUp(payment, "127.0.0.1"))
		})
	}
}

func TestCompleteWeChatTopUpCreditsOnce(t *testing.T) {
	truncateTables(t)
	originalQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500
	t.Cleanup(func() { common.QuotaPerUnit = originalQuotaPerUnit })

	user := User{Username: "wechat-success", Quota: 100}
	require.NoError(t, DB.Create(&user).Error)
	topUp := TopUp{
		UserId:          user.Id,
		Amount:          2,
		Money:           14.6,
		TradeNo:         "wechat-success",
		PaymentMethod:   PaymentProviderWeChatPay,
		PaymentProvider: PaymentProviderWeChatPay,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, DB.Create(&topUp).Error)

	require.NoError(t, CompleteWeChatTopUp(verifiedWeChatPayment(topUp.TradeNo, "wx-success", 1460), "127.0.0.1"))
	require.NoError(t, CompleteWeChatTopUp(verifiedWeChatPayment(topUp.TradeNo, "wx-success", 1460), "127.0.0.1"))

	require.NoError(t, DB.First(&user, user.Id).Error)
	require.NoError(t, DB.First(&topUp, topUp.Id).Error)
	assert.Equal(t, 1100, user.Quota)
	assert.Equal(t, common.TopUpStatusSuccess, topUp.Status)
	assert.NotZero(t, topUp.CompleteTime)
	require.NotNil(t, topUp.ProviderTradeNo)
	assert.Equal(t, "wx-success", *topUp.ProviderTradeNo)
}

func TestCompleteWeChatTopUpRejectsReusedTransactionID(t *testing.T) {
	truncateTables(t)
	originalQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500
	t.Cleanup(func() { common.QuotaPerUnit = originalQuotaPerUnit })

	user := User{Username: "wechat-unique-transaction", Quota: 100}
	require.NoError(t, DB.Create(&user).Error)
	for _, tradeNo := range []string{"wechat-unique-one", "wechat-unique-two"} {
		topUp := TopUp{
			UserId: user.Id, Amount: 2, Money: 14.6, TradeNo: tradeNo,
			ProviderAmount: 1460, ProviderCurrency: "CNY",
			PaymentMethod: PaymentProviderWeChatPay, PaymentProvider: PaymentProviderWeChatPay,
			Status: common.TopUpStatusPending,
		}
		require.NoError(t, DB.Create(&topUp).Error)
	}

	require.NoError(t, CompleteWeChatTopUp(verifiedWeChatPayment("wechat-unique-one", "wx-unique", 1460), "127.0.0.1"))
	err := CompleteWeChatTopUp(verifiedWeChatPayment("wechat-unique-two", "wx-unique", 1460), "127.0.0.1")
	require.Error(t, err)

	require.NoError(t, DB.First(&user, user.Id).Error)
	assert.Equal(t, 1100, user.Quota)
	second := GetTopUpByTradeNo("wechat-unique-two")
	require.NotNil(t, second)
	assert.Equal(t, common.TopUpStatusPending, second.Status)
}

func TestFindPendingWeChatTopUpsUsesProviderStatusAndAge(t *testing.T) {
	truncateTables(t)
	user := User{Username: "wechat-pending-scan"}
	require.NoError(t, DB.Create(&user).Error)

	topUps := []TopUp{
		{UserId: user.Id, TradeNo: "wechat-old", PaymentProvider: PaymentProviderWeChatPay, Status: common.TopUpStatusPending, CreateTime: 100},
		{UserId: user.Id, TradeNo: "wechat-new", PaymentProvider: PaymentProviderWeChatPay, Status: common.TopUpStatusPending, CreateTime: 300},
		{UserId: user.Id, TradeNo: "wechat-successful", PaymentProvider: PaymentProviderWeChatPay, Status: common.TopUpStatusSuccess, CreateTime: 100},
		{UserId: user.Id, TradeNo: "epay-old", PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending, CreateTime: 100},
	}
	for index := range topUps {
		topUps[index].PaymentMethod = topUps[index].PaymentProvider
		require.NoError(t, DB.Create(&topUps[index]).Error)
	}

	found, err := FindPendingWeChatTopUps(200, 200, 100)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.True(t, strings.HasPrefix(found[0].TradeNo, "wechat-old"))

	require.NoError(t, MarkWeChatTopUpChecked("wechat-old", 250))
	found, err = FindPendingWeChatTopUps(200, 200, 100)
	require.NoError(t, err)
	assert.Empty(t, found)
}

func TestPaymentHealthMetricsAndPendingBacklog(t *testing.T) {
	truncateTables(t)
	base := int64(3_600_100)
	require.NoError(t, UpsertPaymentHealthMetric(&PaymentHealthMetric{
		Provider: PaymentProviderWeChatPay, BucketTs: base, QueryAttempt: 2, QueryFailure: 1,
	}))
	require.NoError(t, UpsertPaymentHealthMetric(&PaymentHealthMetric{
		Provider: PaymentProviderWeChatPay, BucketTs: base + 60, QueryAttempt: 3, CallbackVerificationFailure: 1,
	}))
	metric, err := SumPaymentHealthMetrics(PaymentProviderWeChatPay, PaymentHealthBucket(base), PaymentHealthBucket(base))
	require.NoError(t, err)
	assert.Equal(t, int64(5), metric.QueryAttempt)
	assert.Equal(t, int64(1), metric.QueryFailure)
	assert.Equal(t, int64(1), metric.CallbackVerificationFailure)

	user := User{Username: "wechat-backlog"}
	require.NoError(t, DB.Create(&user).Error)
	for _, topUp := range []TopUp{
		{UserId: user.Id, TradeNo: "backlog-old", PaymentProvider: PaymentProviderWeChatPay, Status: common.TopUpStatusPending, CreateTime: 100},
		{UserId: user.Id, TradeNo: "backlog-new", PaymentProvider: PaymentProviderWeChatPay, Status: common.TopUpStatusPending, CreateTime: 200},
		{UserId: user.Id, TradeNo: "backlog-success", PaymentProvider: PaymentProviderWeChatPay, Status: common.TopUpStatusSuccess, CreateTime: 50},
	} {
		require.NoError(t, DB.Create(&topUp).Error)
	}
	backlog, err := GetPendingWeChatTopUpBacklog()
	require.NoError(t, err)
	assert.Equal(t, int64(2), backlog.Count)
	assert.Equal(t, int64(100), backlog.OldestCreateAt)
}

func verifiedWeChatPayment(tradeNo string, transactionID string, total int64) VerifiedWeChatPayment {
	return VerifiedWeChatPayment{
		AppID:         "wx-app-id",
		MchID:         "merchant-id",
		OutTradeNo:    tradeNo,
		TransactionID: transactionID,
		TradeState:    "SUCCESS",
		Total:         total,
		Currency:      "CNY",
	}
}