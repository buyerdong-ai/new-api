package model

import (
	"errors"

	"gorm.io/gorm"
)

const (
	WeChatPayReconciliationRunning   = "running"
	WeChatPayReconciliationSucceeded = "succeeded"
	WeChatPayReconciliationFailed    = "failed"
)

type WeChatPayReconciliation struct {
	ID                   int64  `json:"id" gorm:"primaryKey"`
	Provider             string `json:"provider" gorm:"type:varchar(50);uniqueIndex:idx_payment_reconciliation_provider_date,priority:1"`
	BillDate             string `json:"bill_date" gorm:"type:varchar(10);uniqueIndex:idx_payment_reconciliation_provider_date,priority:2"`
	Status               string `json:"status" gorm:"type:varchar(20);index"`
	ProviderSuccessCount int64  `json:"provider_success_count"`
	LocalSuccessCount    int64  `json:"local_success_count"`
	UnknownOrderCount    int64  `json:"unknown_order_count"`
	MissingOrderCount    int64  `json:"missing_order_count"`
	AmountMismatchCount  int64  `json:"amount_mismatch_count"`
	TransactionMismatchCount int64 `json:"transaction_mismatch_count"`
	DifferenceSample     string `json:"difference_sample" gorm:"type:text"`
	Error                string `json:"error" gorm:"type:text"`
	StartedAt            int64  `json:"started_at"`
	CompletedAt          int64  `json:"completed_at"`
}

func (WeChatPayReconciliation) TableName() string {
	return "wechat_pay_reconciliations"
}

func GetWeChatPayReconciliation(billDate string) (*WeChatPayReconciliation, error) {
	reconciliation := &WeChatPayReconciliation{}
	err := DB.Where("provider = ? AND bill_date = ?", PaymentProviderWeChatPay, billDate).First(reconciliation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return reconciliation, err
}

func SaveWeChatPayReconciliation(reconciliation *WeChatPayReconciliation) error {
	if reconciliation.Provider == "" {
		reconciliation.Provider = PaymentProviderWeChatPay
	}
	return DB.Save(reconciliation).Error
}