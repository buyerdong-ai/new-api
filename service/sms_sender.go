package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const aliyunSMSEndpoint = "https://dysmsapi.aliyuncs.com/"

type SMSCodeSender interface {
	SendVerificationCode(ctx context.Context, phone, code string) error
}

type aliyunSMSCodeSender struct{}

var registrationSMSCodeSender SMSCodeSender = aliyunSMSCodeSender{}

type aliyunSMSResponse struct {
	Code      string `json:"Code"`
	Message   string `json:"Message"`
	RequestID string `json:"RequestId"`
}

func SetRegistrationSMSCodeSender(sender SMSCodeSender) func() {
	previous := registrationSMSCodeSender
	registrationSMSCodeSender = sender
	return func() {
		registrationSMSCodeSender = previous
	}
}

func SendRegistrationSMSCode(ctx context.Context, phone, code string) error {
	if registrationSMSCodeSender == nil {
		return ErrSMSUnavailable
	}
	return registrationSMSCodeSender.SendVerificationCode(ctx, phone, code)
}

func SMSRegistrationReady() bool {
	if err := ensureSMSRedis(); err != nil {
		return false
	}
	return common.GetEnvOrDefaultString("ALIYUN_SMS_ACCESS_KEY_ID", "") != "" &&
		common.GetEnvOrDefaultString("ALIYUN_SMS_ACCESS_KEY_SECRET", "") != "" &&
		common.GetEnvOrDefaultString("ALIYUN_SMS_SIGN_NAME", "") != "" &&
		common.GetEnvOrDefaultString("ALIYUN_SMS_TEMPLATE_CODE", "") != ""
}

func (aliyunSMSCodeSender) SendVerificationCode(ctx context.Context, phone, code string) error {
	accessKeyID := common.GetEnvOrDefaultString("ALIYUN_SMS_ACCESS_KEY_ID", "")
	accessKeySecret := common.GetEnvOrDefaultString("ALIYUN_SMS_ACCESS_KEY_SECRET", "")
	signName := common.GetEnvOrDefaultString("ALIYUN_SMS_SIGN_NAME", "")
	templateCode := common.GetEnvOrDefaultString("ALIYUN_SMS_TEMPLATE_CODE", "")
	if accessKeyID == "" || accessKeySecret == "" || signName == "" || templateCode == "" {
		return ErrSMSUnavailable
	}

	templateParam, err := common.Marshal(map[string]string{"code": code})
	if err != nil {
		return err
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return err
	}
	parameters := map[string]string{
		"AccessKeyId":      accessKeyID,
		"Action":           "SendSms",
		"Format":           "JSON",
		"PhoneNumbers":     strings.TrimPrefix(phone, "+86"),
		"SignName":         signName,
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   hex.EncodeToString(nonceBytes),
		"SignatureVersion": "1.0",
		"TemplateCode":     templateCode,
		"TemplateParam":    string(templateParam),
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"Version":          "2017-05-25",
	}
	canonicalQuery := aliyunCanonicalQuery(parameters)
	stringToSign := http.MethodPost + "&%2F&" + aliyunPercentEncode(canonicalQuery)
	signatureHMAC := hmac.New(sha1.New, []byte(accessKeySecret+"&"))
	_, _ = signatureHMAC.Write([]byte(stringToSign))
	parameters["Signature"] = base64.StdEncoding.EncodeToString(signatureHMAC.Sum(nil))

	form := url.Values{}
	for key, value := range parameters {
		form.Set(key, value)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, aliyunSMSEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := GetHttpClient().Do(request)
	if err != nil {
		return fmt.Errorf("send SMS request: %w", err)
	}
	defer response.Body.Close()
	var result aliyunSMSResponse
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if err := common.DecodeJson(response.Body, &result); err == nil && (result.Code != "" || result.Message != "") {
			if result.Message == "" {
				result.Message = result.Code
			}
			return fmt.Errorf("send SMS request: Aliyun %s: %s", result.Code, result.Message)
		}
		return fmt.Errorf("send SMS request: unexpected status %d", response.StatusCode)
	}
	if err := common.DecodeJson(response.Body, &result); err != nil {
		return fmt.Errorf("decode SMS response: %w", err)
	}
	if result.Code != "OK" {
		if result.Message == "" {
			result.Message = result.Code
		}
		return errors.New(result.Message)
	}
	return nil
}

func aliyunCanonicalQuery(parameters map[string]string) string {
	keys := make([]string, 0, len(parameters))
	for key := range parameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, aliyunPercentEncode(key)+"="+aliyunPercentEncode(parameters[key]))
	}
	return strings.Join(parts, "&")
}

func aliyunPercentEncode(value string) string {
	encoded := url.QueryEscape(value)
	encoded = strings.ReplaceAll(encoded, "+", "%20")
	encoded = strings.ReplaceAll(encoded, "*", "%2A")
	return strings.ReplaceAll(encoded, "%7E", "~")
}