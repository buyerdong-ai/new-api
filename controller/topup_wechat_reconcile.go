package controller

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/shopspring/decimal"
	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
)

const (
	weChatTradeBillURL = "https://api.mch.weixin.qq.com/v3/bill/tradebill"
	maxWeChatBillBytes = 100 << 20
	maxReconciliationSamples = 20
)

type weChatTradeBillResponse struct {
	HashType    string `json:"hash_type"`
	HashValue   string `json:"hash_value"`
	DownloadURL string `json:"download_url"`
}

type weChatTradeBillEntry struct {
	TradeNo       string
	TransactionID string
	State         string
	AmountFen     int64
}

type weChatReconciliationDifference struct {
	Type          string `json:"type"`
	TradeNo       string `json:"trade_no"`
	TransactionID string `json:"transaction_id,omitempty"`
}

func verifyWeChatBillDigest(data []byte, hashType string, expected string) error {
	var actual string
	switch strings.ToUpper(strings.TrimSpace(hashType)) {
	case "SHA1":
		digest := sha1.Sum(data)
		actual = hex.EncodeToString(digest[:])
	case "SHA256":
		digest := sha256.Sum256(data)
		actual = hex.EncodeToString(digest[:])
	default:
		return fmt.Errorf("unsupported WeChat bill hash type %q", hashType)
	}
	if !strings.EqualFold(actual, strings.TrimSpace(expected)) {
		return errors.New("WeChat bill digest mismatch")
	}
	return nil
}

func normalizeWeChatBillCell(value string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(value, "\ufeff"), "`"))
}

func parseWeChatTradeBill(data []byte) ([]weChatTradeBillEntry, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse WeChat trade bill CSV: %w", err)
	}

	headerIndex := -1
	columns := map[string]int{}
	for index, record := range records {
		for column, value := range record {
			switch normalizeWeChatBillCell(value) {
			case "微信支付订单号", "微信订单号":
				columns["transaction_id"] = column
			case "商户订单号":
				columns["trade_no"] = column
			case "交易状态":
				columns["state"] = column
			case "总金额", "订单金额":
				columns["amount"] = column
			}
		}
		if len(columns) == 4 {
			headerIndex = index
			break
		}
	}
	if headerIndex < 0 {
		return nil, errors.New("WeChat trade bill is missing required columns")
	}

	entries := make([]weChatTradeBillEntry, 0, len(records)-headerIndex-1)
	for _, record := range records[headerIndex+1:] {
		maxColumn := columns["transaction_id"]
		for _, column := range columns {
			if column > maxColumn {
				maxColumn = column
			}
		}
		if len(record) <= maxColumn {
			continue
		}
		tradeNo := normalizeWeChatBillCell(record[columns["trade_no"]])
		transactionID := normalizeWeChatBillCell(record[columns["transaction_id"]])
		if tradeNo == "" || transactionID == "" {
			continue
		}
		amount, err := decimal.NewFromString(normalizeWeChatBillCell(record[columns["amount"]]))
		if err != nil {
			return nil, fmt.Errorf("invalid amount for WeChat order %s: %w", tradeNo, err)
		}
		amountFen := amount.Mul(decimal.NewFromInt(100))
		if !amountFen.Equal(amountFen.Truncate(0)) || amountFen.IsNegative() {
			return nil, fmt.Errorf("invalid fractional amount for WeChat order %s", tradeNo)
		}
		entries = append(entries, weChatTradeBillEntry{
			TradeNo: tradeNo, TransactionID: transactionID,
			State: normalizeWeChatBillCell(record[columns["state"]]), AmountFen: amountFen.IntPart(),
		})
	}
	return entries, nil
}

func reconcileWeChatTradeBill(entries []weChatTradeBillEntry, local []model.TopUp) (model.WeChatPayReconciliation, error) {
	reconciliation := model.WeChatPayReconciliation{Provider: model.PaymentProviderWeChatPay}
	providerByTradeNo := make(map[string]weChatTradeBillEntry, len(entries))
	for _, entry := range entries {
		if entry.State != "SUCCESS" && entry.State != "支付成功" {
			continue
		}
		if _, exists := providerByTradeNo[entry.TradeNo]; exists {
			return reconciliation, fmt.Errorf("duplicate merchant order in WeChat bill: %s", entry.TradeNo)
		}
		providerByTradeNo[entry.TradeNo] = entry
	}
	reconciliation.ProviderSuccessCount = int64(len(providerByTradeNo))
	reconciliation.LocalSuccessCount = int64(len(local))
	localByTradeNo := make(map[string]model.TopUp, len(local))
	for _, topUp := range local {
		localByTradeNo[topUp.TradeNo] = topUp
	}

	differences := make([]weChatReconciliationDifference, 0, maxReconciliationSamples)
	addDifference := func(difference weChatReconciliationDifference) {
		if len(differences) < maxReconciliationSamples {
			differences = append(differences, difference)
		}
	}
	for tradeNo, entry := range providerByTradeNo {
		topUp, exists := localByTradeNo[tradeNo]
		if !exists {
			reconciliation.UnknownOrderCount++
			addDifference(weChatReconciliationDifference{Type: "provider_order_missing_locally", TradeNo: tradeNo, TransactionID: entry.TransactionID})
			continue
		}
		if topUp.ProviderAmount != entry.AmountFen || topUp.ProviderCurrency != "CNY" {
			reconciliation.AmountMismatchCount++
			addDifference(weChatReconciliationDifference{Type: "amount_or_currency_mismatch", TradeNo: tradeNo, TransactionID: entry.TransactionID})
		}
		if topUp.ProviderTradeNo == nil || *topUp.ProviderTradeNo != entry.TransactionID {
			reconciliation.TransactionMismatchCount++
			addDifference(weChatReconciliationDifference{Type: "transaction_id_mismatch", TradeNo: tradeNo, TransactionID: entry.TransactionID})
		}
	}
	for tradeNo, topUp := range localByTradeNo {
		if _, exists := providerByTradeNo[tradeNo]; !exists {
			reconciliation.MissingOrderCount++
			difference := weChatReconciliationDifference{Type: "local_success_missing_from_provider", TradeNo: tradeNo}
			if topUp.ProviderTradeNo != nil {
				difference.TransactionID = *topUp.ProviderTradeNo
			}
			addDifference(difference)
		}
	}
	sample, err := common.Marshal(differences)
	if err != nil {
		return reconciliation, err
	}
	reconciliation.DifferenceSample = string(sample)
	return reconciliation, nil
}

func downloadWeChatTradeBill(ctx context.Context, billDate string) ([]weChatTradeBillEntry, error) {
	client, err := newWeChatPayClient(ctx)
	if err != nil {
		return nil, err
	}
	requestURL := weChatTradeBillURL + "?bill_date=" + url.QueryEscape(billDate) + "&bill_type=ALL"
	result, err := client.Get(ctx, requestURL)
	if err != nil {
		return nil, fmt.Errorf("request WeChat trade bill: %w", err)
	}
	defer result.Response.Body.Close()
	response := weChatTradeBillResponse{}
	if err := common.DecodeJson(result.Response.Body, &response); err != nil {
		return nil, fmt.Errorf("decode WeChat trade bill response: %w", err)
	}
	downloadURL, err := url.Parse(response.DownloadURL)
	if err != nil || downloadURL.Scheme != "https" || downloadURL.Hostname() != "api.mch.weixin.qq.com" {
		return nil, errors.New("invalid WeChat trade bill download URL")
	}
	privateKey, err := utils.LoadPrivateKey(setting.WeChatPayMchPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("load merchant private key: %w", err)
	}
	downloadClient, err := core.NewClient(ctx,
		option.WithMerchantCredential(setting.WeChatPayMchID, setting.WeChatPayMchCertificateSerial, privateKey),
		option.WithoutValidator(),
	)
	if err != nil {
		return nil, fmt.Errorf("create WeChat bill download client: %w", err)
	}
	downloadResult, err := downloadClient.Get(ctx, response.DownloadURL)
	if err != nil {
		return nil, fmt.Errorf("download WeChat trade bill: %w", err)
	}
	defer downloadResult.Response.Body.Close()
	bill, err := io.ReadAll(io.LimitReader(downloadResult.Response.Body, maxWeChatBillBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read WeChat trade bill: %w", err)
	}
	if len(bill) > maxWeChatBillBytes {
		return nil, errors.New("WeChat trade bill exceeds size limit")
	}
	if err := verifyWeChatBillDigest(bill, response.HashType, response.HashValue); err != nil {
		return nil, err
	}
	return parseWeChatTradeBill(bill)
}

func runWeChatPayReconciliation(ctx context.Context, billDate string) (*model.WeChatPayReconciliation, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	day, err := time.ParseInLocation("2006-01-02", billDate, location)
	if err != nil {
		return nil, fmt.Errorf("invalid bill date: %w", err)
	}
	reconciliation, err := model.GetWeChatPayReconciliation(billDate)
	if err != nil {
		return nil, err
	}
	if reconciliation != nil && reconciliation.Status == model.WeChatPayReconciliationSucceeded {
		return reconciliation, nil
	}
	if reconciliation == nil {
		reconciliation = &model.WeChatPayReconciliation{Provider: model.PaymentProviderWeChatPay, BillDate: billDate}
	}
	reconciliation.Status = model.WeChatPayReconciliationRunning
	reconciliation.StartedAt = common.GetTimestamp()
	reconciliation.CompletedAt = 0
	reconciliation.Error = ""
	if err := model.SaveWeChatPayReconciliation(reconciliation); err != nil {
		return nil, err
	}

	entries, err := downloadWeChatTradeBill(ctx, billDate)
	if err == nil {
		var local []model.TopUp
		local, err = model.FindSuccessfulWeChatTopUps(day.Unix(), day.AddDate(0, 0, 1).Unix())
		if err == nil {
			var result model.WeChatPayReconciliation
			result, err = reconcileWeChatTradeBill(entries, local)
			if err == nil {
				result.ID = reconciliation.ID
				result.BillDate = billDate
				result.Status = model.WeChatPayReconciliationSucceeded
				result.StartedAt = reconciliation.StartedAt
				*reconciliation = result
			}
		}
	}
	reconciliation.CompletedAt = common.GetTimestamp()
	if err != nil {
		reconciliation.Status = model.WeChatPayReconciliationFailed
		reconciliation.Error = err.Error()
	}
	if saveErr := model.SaveWeChatPayReconciliation(reconciliation); saveErr != nil {
		return reconciliation, saveErr
	}
	if err != nil {
		return reconciliation, err
	}
	differenceCount := reconciliation.UnknownOrderCount + reconciliation.MissingOrderCount + reconciliation.AmountMismatchCount + reconciliation.TransactionMismatchCount
	if differenceCount > 0 {
		service.NotifyRootUser("wechat_pay_reconciliation_difference", "微信支付每日对账存在差异", fmt.Sprintf("账单日期 %s，共发现 %d 项差异：微信有本地无 %d，本地成功微信无 %d，金额/币种不一致 %d，流水号不一致 %d。", billDate, differenceCount, reconciliation.UnknownOrderCount, reconciliation.MissingOrderCount, reconciliation.AmountMismatchCount, reconciliation.TransactionMismatchCount))
	}
	return reconciliation, nil
}