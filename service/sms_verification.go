package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	SMSRegisterPurpose = "sms_register"
	smsCodeTTL         = 5 * time.Minute
	smsClaimTTL        = 30 * time.Second
)

var (
	ErrSMSUnavailable       = errors.New("SMS verification is unavailable")
	ErrSMSRateLimited       = errors.New("SMS verification rate limit exceeded")
	ErrSMSCodeInvalid       = errors.New("SMS verification code is invalid or expired")
	ErrSMSCodeClaimMismatch = errors.New("SMS verification claim does not match")
)

const smsRateLimitScript = `
for index = 1, #KEYS do
  local count = tonumber(redis.call('GET', KEYS[index]) or '0')
  if count >= tonumber(ARGV[index * 2 - 1]) then
    return 0
  end
end
for index = 1, #KEYS do
  local count = redis.call('INCR', KEYS[index])
  if count == 1 then
    redis.call('EXPIRE', KEYS[index], ARGV[index * 2])
  end
end
return 1
`

const smsActivateScript = `
local value = redis.call('GET', KEYS[1])
if value ~= 'pending:' .. ARGV[1] then
  return 0
end
redis.call('SET', KEYS[1], 'active:' .. ARGV[1], 'EX', ARGV[2])
redis.call('DEL', KEYS[2])
return 1
`

const smsClaimScript = `
local value = redis.call('GET', KEYS[1])
if not value then
  return 0
end
if value == 'active:' .. ARGV[1] then
	local remaining = redis.call('PTTL', KEYS[1])
	if remaining <= 0 then
		return 0
	end
	local now = redis.call('TIME')
	local expires_at = now[1] * 1000 + math.floor(now[2] / 1000) + remaining
	local claim_ttl = math.min(remaining, tonumber(ARGV[3]))
	redis.call('SET', KEYS[1], 'claimed:' .. ARGV[2] .. ':' .. expires_at .. ':' .. ARGV[1], 'PX', claim_ttl)
  redis.call('DEL', KEYS[2])
  return 1
end
if string.sub(value, 1, 7) == 'active:' then
  local attempts = redis.call('INCR', KEYS[2])
  if attempts == 1 then
    redis.call('EXPIRE', KEYS[2], ARGV[4])
  end
  if attempts >= tonumber(ARGV[5]) then
    redis.call('DEL', KEYS[1])
  end
  return -1
end
return -2
`

const smsFinishClaimScript = `
local value = redis.call('GET', KEYS[1])
local prefix = 'claimed:' .. ARGV[1] .. ':'
if not value or string.sub(value, 1, string.len(prefix)) ~= prefix then
  return 0
end
if ARGV[2] == 'consume' then
  redis.call('DEL', KEYS[1])
else
	local remainder = string.sub(value, string.len(prefix) + 1)
	local separator = string.find(remainder, ':')
	if not separator then
		return 0
	end
	local expires_at = tonumber(string.sub(remainder, 1, separator - 1))
	local digest = string.sub(remainder, separator + 1)
	local now = redis.call('TIME')
	local remaining = expires_at - (now[1] * 1000 + math.floor(now[2] / 1000))
	if remaining <= 0 then
		redis.call('DEL', KEYS[1])
		return 1
	end
	redis.call('SET', KEYS[1], 'active:' .. digest, 'PX', remaining)
end
return 1
`

func GenerateSMSCode() (string, error) {
	code := make([]byte, 6)
	for index := range code {
		digit, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		code[index] = byte('0' + digit.Int64())
	}
	return string(code), nil
}

func smsPhoneDigest(phone string) string {
	return common.GenerateHMAC("sms:phone:" + phone)
}

func smsCodeDigest(phone, purpose, code string) string {
	return common.GenerateHMAC("sms:code:" + purpose + ":" + phone + ":" + code)
}

func smsVerificationKey(phone, purpose string) string {
	return "verification:sms:v1:" + purpose + ":" + smsPhoneDigest(phone)
}

func ensureSMSRedis() error {
	if !common.RedisEnabled || common.RDB == nil {
		return ErrSMSUnavailable
	}
	return nil
}

func TakeSMSRateLimits(ctx context.Context, phone, clientIP string) error {
	if err := ensureSMSRedis(); err != nil {
		return err
	}
	phoneDigest := smsPhoneDigest(phone)
	ipDigest := common.GenerateHMAC("sms:ip:" + clientIP)
	keys := []string{
		"verification:sms:limit:cooldown:" + phoneDigest,
		"verification:sms:limit:phone:hour:" + phoneDigest,
		"verification:sms:limit:phone:day:" + phoneDigest,
		"verification:sms:limit:ip:hour:" + ipDigest,
		"verification:sms:limit:ip:day:" + ipDigest,
	}
	result, err := common.RDB.Eval(ctx, smsRateLimitScript, keys,
		1, 60,
		5, 60*60,
		10, 24*60*60,
		20, 60*60,
		50, 24*60*60,
	).Int()
	if err != nil {
		return fmt.Errorf("take SMS rate limits: %w", err)
	}
	if result != 1 {
		return ErrSMSRateLimited
	}
	return nil
}

func SavePendingSMSCode(ctx context.Context, phone, purpose, code string) error {
	if err := ensureSMSRedis(); err != nil {
		return err
	}
	digest := smsCodeDigest(phone, purpose, code)
	return common.RDB.Set(ctx, smsVerificationKey(phone, purpose), "pending:"+digest, smsCodeTTL).Err()
}

func ActivateSMSCode(ctx context.Context, phone, purpose, code string) error {
	if err := ensureSMSRedis(); err != nil {
		return err
	}
	digest := smsCodeDigest(phone, purpose, code)
	result, err := common.RDB.Eval(ctx, smsActivateScript, []string{
		smsVerificationKey(phone, purpose),
		smsVerificationKey(phone, purpose) + ":attempts",
	}, digest, int64(smsCodeTTL/time.Second)).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return ErrSMSCodeInvalid
	}
	return nil
}

func DeleteSMSCode(ctx context.Context, phone, purpose string) error {
	if err := ensureSMSRedis(); err != nil {
		return err
	}
	return common.RDB.Del(ctx, smsVerificationKey(phone, purpose)).Err()
}

func ClaimSMSCode(ctx context.Context, phone, purpose, code, claimID string) error {
	if err := ensureSMSRedis(); err != nil {
		return err
	}
	digest := smsCodeDigest(phone, purpose, code)
	result, err := common.RDB.Eval(ctx, smsClaimScript, []string{
		smsVerificationKey(phone, purpose),
		smsVerificationKey(phone, purpose) + ":attempts",
	}, digest, claimID, int64(smsClaimTTL/time.Millisecond), int64(smsCodeTTL/time.Second), 5).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return ErrSMSCodeInvalid
	}
	return nil
}

func FinishSMSCodeClaim(ctx context.Context, phone, purpose, claimID string, consume bool) error {
	if err := ensureSMSRedis(); err != nil {
		return err
	}
	action := "release"
	if consume {
		action = "consume"
	}
	result, err := common.RDB.Eval(ctx, smsFinishClaimScript, []string{
		smsVerificationKey(phone, purpose),
	}, claimID, action).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return ErrSMSCodeClaimMismatch
	}
	return nil
}