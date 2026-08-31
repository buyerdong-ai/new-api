package controller

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/verifiers"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/native"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
)

const maxWeChatTopUp int64 = 1000

var errWeChatOrderPending = errors.New("wechat pay order is not successful")

func recordWeChatPaymentHealth(ctx context.Context, metric model.PaymentHealthMetric) {
	metric.Provider = model.PaymentProviderWeChatPay
	metric.BucketTs = common.GetTimestamp()
	if err := model.UpsertPaymentHealthMetric(&metric); err != nil {
		logger.LogWarn(ctx, "微信支付 健康指标写入失败: "+err.Error())
	}
}

func validateWeChatTopUpAmount(amount int64) error {
	if amount < getMinTopup() {
		return fmt.Errorf("充值数量不能小于 %d", getMinTopup())
	}
	if amount > maxWeChatTopUp {
		return fmt.Errorf("充值数量不能大于 %d", maxWeChatTopUp)
	}
	return nil
}

func newWeChatPayClient(ctx context.Context) (*core.Client, error) {
	privateKey, err := utils.LoadPrivateKey(setting.WeChatPayMchPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("load merchant private key: %w", err)
	}
	if strings.TrimSpace(setting.WeChatPayPublicKeyID) != "" && strings.TrimSpace(setting.WeChatPayPublicKey) != "" {
		publicKey, err := utils.LoadPublicKey(setting.WeChatPayPublicKey)
		if err != nil {
			return nil, fmt.Errorf("load WeChat Pay public key: %w", err)
		}
		return core.NewClient(ctx, option.WithWechatPayPublicKeyAuthCipher(
			setting.WeChatPayMchID,
			setting.WeChatPayMchCertificateSerial,
			privateKey,
			setting.WeChatPayPublicKeyID,
			publicKey,
		))
	}
	platformCertificate, err := utils.LoadCertificate(setting.WeChatPayPlatformCertificate)
	if err != nil {
		return nil, fmt.Errorf("load platform certificate: %w", err)
	}
	return core.NewClient(ctx,
		option.WithMerchantCredential(setting.WeChatPayMchID, setting.WeChatPayMchCertificateSerial, privateKey),
		option.WithWechatPayCertificate([]*x509.Certificate{platformCertificate}),
	)
}

func verifiedWeChatPaymentFromTransaction(transaction *payments.Transaction) (model.VerifiedWeChatPayment, error) {
	if transaction == nil || transaction.Appid == nil || *transaction.Appid != setting.WeChatPayAppID ||
		transaction.Mchid == nil || *transaction.Mchid != setting.WeChatPayMchID ||
		transaction.OutTradeNo == nil || strings.TrimSpace(*transaction.OutTradeNo) == "" ||
		transaction.TransactionId == nil || strings.TrimSpace(*transaction.TransactionId) == "" ||
		transaction.TradeState == nil || *transaction.TradeState != "SUCCESS" ||
		transaction.Amount == nil || transaction.Amount.Total == nil || *transaction.Amount.Total <= 0 ||
		transaction.Amount.Currency == nil || *transaction.Amount.Currency != "CNY" {
		return model.VerifiedWeChatPayment{}, errors.New("微信支付交易校验失败")
	}
	return model.VerifiedWeChatPayment{
		AppID:         *transaction.Appid,
		MchID:         *transaction.Mchid,
		OutTradeNo:    *transaction.OutTradeNo,
		TransactionID: *transaction.TransactionId,
		TradeState:    *transaction.TradeState,
		Total:         *transaction.Amount.Total,
		Currency:      *transaction.Amount.Currency,
	}, nil
}

func validateWeChatTransactionIdentity(transaction *payments.Transaction, tradeNo string) error {
	if transaction == nil || transaction.Appid == nil || *transaction.Appid != setting.WeChatPayAppID ||
		transaction.Mchid == nil || *transaction.Mchid != setting.WeChatPayMchID ||
		transaction.OutTradeNo == nil || *transaction.OutTradeNo != tradeNo ||
		transaction.TradeState == nil {
		return errors.New("微信支付查单结果身份校验失败")
	}
	return nil
}

func queryAndCompleteWeChatTopUp(ctx context.Context, tradeNo string, callerIP string) error {
	recordWeChatPaymentHealth(ctx, model.PaymentHealthMetric{QueryAttempt: 1})
	failed := true
	defer func() {
		if failed {
			recordWeChatPaymentHealth(ctx, model.PaymentHealthMetric{QueryFailure: 1})
		}
	}()
	defer func() {
		if err := model.MarkWeChatTopUpChecked(tradeNo, common.GetTimestamp()); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("微信支付 更新查单时间失败 trade_no=%s error=%q", tradeNo, err.Error()))
		}
	}()
	client, err := newWeChatPayClient(ctx)
	if err != nil {
		return err
	}
	transaction, _, err := (&native.NativeApiService{Client: client}).QueryOrderByOutTradeNo(ctx, native.QueryOrderByOutTradeNoRequest{
		OutTradeNo: core.String(tradeNo),
		Mchid:      core.String(setting.WeChatPayMchID),
	})
	if err != nil {
		return fmt.Errorf("query WeChat Pay order: %w", err)
	}
	if err := validateWeChatTransactionIdentity(transaction, tradeNo); err != nil {
		return err
	}
	if *transaction.TradeState != "SUCCESS" {
		if *transaction.TradeState == "CLOSED" || *transaction.TradeState == "REVOKED" || *transaction.TradeState == "PAYERROR" {
			if err := model.UpdatePendingTopUpStatus(tradeNo, model.PaymentProviderWeChatPay, common.TopUpStatusFailed); err != nil &&
				!errors.Is(err, model.ErrTopUpStatusInvalid) {
				return err
			}
		}
		failed = false
		return fmt.Errorf("%w: %s", errWeChatOrderPending, *transaction.TradeState)
	}
	payment, err := verifiedWeChatPaymentFromTransaction(transaction)
	if err != nil {
		return err
	}
	err = model.CompleteWeChatTopUp(payment, callerIP)
	if errors.Is(err, model.ErrDuplicateProviderTradeNo) {
		recordWeChatPaymentHealth(ctx, model.PaymentHealthMetric{DuplicateTransaction: 1})
		service.NotifyRootUser("wechat_pay_duplicate_transaction", "微信支付流水号冲突", fmt.Sprintf("订单 %s 使用了已归属其他订单的微信流水号，请立即核查。", tradeNo))
	}
	if err == nil {
		failed = false
	}
	return err
}

func RequestWeChatPay(c *gin.Context) {
	if !isWeChatPayTopUpEnabled() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "当前管理员未配置微信支付"})
		return
	}

	var req AmountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if err := validateWeChatTopUpAmount(req.Amount); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return
	}

	userID := c.GetInt("id")
	group, err := model.GetUserGroup(userID, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	payMoney := getPayMoney(req.Amount, group)
	payFen := decimal.NewFromFloat(payMoney).Mul(decimal.NewFromInt(100)).Round(0).IntPart()
	if payFen <= 0 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}

	tradeNo := fmt.Sprintf("USR%dWX%s%d", userID, common.GetRandomString(6), time.Now().Unix())
	amount := req.Amount
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		amount = decimal.NewFromInt(req.Amount).Div(decimal.NewFromFloat(common.QuotaPerUnit)).IntPart()
	}
	topUp := &model.TopUp{
		UserId: userID, Amount: amount, Money: payMoney, TradeNo: tradeNo,
		ProviderAmount: payFen, ProviderCurrency: "CNY",
		PaymentMethod: model.PaymentProviderWeChatPay, PaymentProvider: model.PaymentProviderWeChatPay,
		CreateTime: time.Now().Unix(), Status: common.TopUpStatusPending,
	}
	if err := topUp.Insert(); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("微信支付 创建充值订单失败 user_id=%d trade_no=%s error=%q", userID, tradeNo, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	client, err := newWeChatPayClient(c.Request.Context())
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("微信支付 初始化客户端失败 trade_no=%s error=%q", tradeNo, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "微信支付配置无效"})
		return
	}

	notifyURL := strings.TrimRight(service.GetCallbackAddress(), "/") + "/api/wechat-pay/webhook"
	response, _, err := (&native.NativeApiService{Client: client}).Prepay(c.Request.Context(), native.PrepayRequest{
		Appid: core.String(setting.WeChatPayAppID), Mchid: core.String(setting.WeChatPayMchID),
		Description: core.String(fmt.Sprintf("TUC%d", req.Amount)), OutTradeNo: core.String(tradeNo),
		NotifyUrl: core.String(notifyURL), Amount: &native.Amount{Total: core.Int64(payFen), Currency: core.String("CNY")},
	})
	if err != nil || response.CodeUrl == nil || strings.TrimSpace(*response.CodeUrl) == "" {
		queryErr := queryAndCompleteWeChatTopUp(c.Request.Context(), tradeNo, c.ClientIP())
		logger.LogError(c.Request.Context(), fmt.Sprintf("微信支付 Native 下单失败 trade_no=%s error=%v", tradeNo, err))
		if queryErr != nil {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("微信支付 预下单结果不确定，保留待支付订单 trade_no=%s query_error=%q", tradeNo, queryErr.Error()))
		}
		message := "拉起支付失败"
		if err != nil && strings.Contains(err.Error(), "certificate[PUB_KEY_ID_") && strings.Contains(err.Error(), "not found in verifier") {
			message = "微信支付返回公钥签名，请在支付设置中配置对应的微信支付公钥 ID 和 PEM 公钥"
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": message})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{"code_url": *response.CodeUrl, "trade_no": tradeNo}})
}

func GetWeChatPayStatus(c *gin.Context) {
	tradeNo := strings.TrimSpace(c.Param("trade_no"))
	topUp := model.GetTopUpByTradeNo(tradeNo)
	if topUp == nil || topUp.UserId != c.GetInt("id") || topUp.PaymentProvider != model.PaymentProviderWeChatPay {
		c.JSON(http.StatusNotFound, gin.H{"message": "error", "data": "订单不存在"})
		return
	}

	if topUp.Status == common.TopUpStatusPending {
		err := queryAndCompleteWeChatTopUp(c.Request.Context(), tradeNo, c.ClientIP())
		if err != nil && !errors.Is(err, errWeChatOrderPending) {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("微信支付 用户查单未结算 trade_no=%s error=%q", tradeNo, err.Error()))
		}
		topUp = model.GetTopUpByTradeNo(tradeNo)
	}

	if topUp == nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "error", "data": "订单不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{"trade_no": topUp.TradeNo, "status": topUp.Status}})
}

func WeChatPayWebhook(c *gin.Context) {
	if !isWeChatPayWebhookEnabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "FAIL", "message": "payment gateway unavailable"})
		return
	}
	var verifier auth.Verifier
	if strings.TrimSpace(setting.WeChatPayPublicKeyID) != "" && strings.TrimSpace(setting.WeChatPayPublicKey) != "" {
		publicKey, err := utils.LoadPublicKey(setting.WeChatPayPublicKey)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": "FAIL", "message": "invalid payment configuration"})
			return
		}
		verifier = verifiers.NewSHA256WithRSAPubkeyVerifier(setting.WeChatPayPublicKeyID, *publicKey)
	} else {
		platformCertificate, err := utils.LoadCertificate(setting.WeChatPayPlatformCertificate)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": "FAIL", "message": "invalid payment configuration"})
			return
		}
		verifier = verifiers.NewSHA256WithRSAVerifier(core.NewCertificateMapWithList([]*x509.Certificate{platformCertificate}))
	}
	handler, err := notify.NewRSANotifyHandler(setting.WeChatPayAPIv3Key, verifier)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": "FAIL", "message": "invalid payment configuration"})
		return
	}

	transaction := new(payments.Transaction)
	if _, err := handler.ParseNotifyRequest(c.Request.Context(), c.Request, transaction); err != nil {
		logger.LogWarn(c.Request.Context(), "微信支付 回调验签或解密失败: "+err.Error())
		recordWeChatPaymentHealth(c.Request.Context(), model.PaymentHealthMetric{CallbackVerificationFailure: 1})
		service.NotifyRootUser("wechat_pay_callback_verification_failure", "微信支付回调验签失败", "检测到微信支付回调验签或解密失败，请检查支付网关日志和平台证书配置。")
		c.JSON(http.StatusUnauthorized, gin.H{"code": "FAIL", "message": "invalid notification"})
		return
	}
	payment, err := verifiedWeChatPaymentFromTransaction(transaction)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "FAIL", "message": "invalid transaction"})
		return
	}
	if err := model.CompleteWeChatTopUp(payment, c.ClientIP()); err != nil {
		if errors.Is(err, model.ErrDuplicateProviderTradeNo) {
			recordWeChatPaymentHealth(c.Request.Context(), model.PaymentHealthMetric{DuplicateTransaction: 1})
			service.NotifyRootUser("wechat_pay_duplicate_transaction", "微信支付流水号冲突", fmt.Sprintf("订单 %s 使用了已归属其他订单的微信流水号，请立即核查。", payment.OutTradeNo))
		}
		if errors.Is(err, model.ErrTopUpNotFound) || errors.Is(err, model.ErrPaymentMethodMismatch) {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("微信支付 回调订单校验失败 trade_no=%s error=%q", payment.OutTradeNo, err.Error()))
		}
		c.JSON(http.StatusInternalServerError, gin.H{"code": "FAIL", "message": "processing failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": "SUCCESS", "message": "成功"})
}
