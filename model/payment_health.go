package model

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PaymentHealthMetric struct {
	ID                          int64  `json:"id" gorm:"primaryKey"`
	Provider                    string `json:"provider" gorm:"type:varchar(50);uniqueIndex:idx_payment_health_provider_bucket,priority:1"`
	BucketTs                    int64  `json:"bucket_ts" gorm:"uniqueIndex:idx_payment_health_provider_bucket,priority:2;index"`
	CallbackVerificationFailure int64  `json:"callback_verification_failure"`
	DuplicateTransaction        int64  `json:"duplicate_transaction"`
	QueryAttempt                int64  `json:"query_attempt"`
	QueryFailure                int64  `json:"query_failure"`
}

func (PaymentHealthMetric) TableName() string {
	return "payment_health_metrics"
}

func PaymentHealthBucket(timestamp int64) int64 {
	return time.Unix(timestamp, 0).UTC().Truncate(time.Hour).Unix()
}

func UpsertPaymentHealthMetric(metric *PaymentHealthMetric) error {
	if metric == nil {
		return nil
	}
	metric.BucketTs = PaymentHealthBucket(metric.BucketTs)
	return DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "provider"}, {Name: "bucket_ts"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"callback_verification_failure": gorm.Expr("payment_health_metrics.callback_verification_failure + ?", metric.CallbackVerificationFailure),
			"duplicate_transaction":         gorm.Expr("payment_health_metrics.duplicate_transaction + ?", metric.DuplicateTransaction),
			"query_attempt":                 gorm.Expr("payment_health_metrics.query_attempt + ?", metric.QueryAttempt),
			"query_failure":                 gorm.Expr("payment_health_metrics.query_failure + ?", metric.QueryFailure),
		}),
	}).Create(metric).Error
}

func SumPaymentHealthMetrics(provider string, startTs int64, endTs int64) (*PaymentHealthMetric, error) {
	metric := &PaymentHealthMetric{Provider: provider}
	err := DB.Model(&PaymentHealthMetric{}).
		Select("COALESCE(SUM(callback_verification_failure), 0) AS callback_verification_failure, COALESCE(SUM(duplicate_transaction), 0) AS duplicate_transaction, COALESCE(SUM(query_attempt), 0) AS query_attempt, COALESCE(SUM(query_failure), 0) AS query_failure").
		Where("provider = ? AND bucket_ts >= ? AND bucket_ts <= ?", provider, startTs, endTs).
		Scan(metric).Error
	return metric, err
}