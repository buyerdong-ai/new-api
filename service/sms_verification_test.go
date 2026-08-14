package service

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useSMSVerificationMiniRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()

	previousRedisEnabled := common.RedisEnabled
	previousRedisClient := common.RDB
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	require.NoError(t, client.Ping(context.Background()).Err())

	common.RedisEnabled = true
	common.RDB = client
	t.Cleanup(func() {
		_ = client.Close()
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRedisClient
	})
	return server
}

func TestGenerateSMSCodeReturnsSixDigits(t *testing.T) {
	code, err := GenerateSMSCode()
	require.NoError(t, err)
	assert.Regexp(t, `^\d{6}$`, code)
}

func TestSMSCodeMustActivateBeforeClaimAndCanOnlyBeConsumedOnce(t *testing.T) {
	useSMSVerificationMiniRedis(t)
	ctx := context.Background()
	phone := "+8613800138000"
	code := "123456"

	require.NoError(t, SavePendingSMSCode(ctx, phone, SMSRegisterPurpose, code))
	assert.ErrorIs(t, ClaimSMSCode(ctx, phone, SMSRegisterPurpose, code, "claim-1"), ErrSMSCodeInvalid)
	require.NoError(t, ActivateSMSCode(ctx, phone, SMSRegisterPurpose, code))
	require.NoError(t, ClaimSMSCode(ctx, phone, SMSRegisterPurpose, code, "claim-1"))
	assert.ErrorIs(t, ClaimSMSCode(ctx, phone, SMSRegisterPurpose, code, "claim-2"), ErrSMSCodeInvalid)
	require.NoError(t, FinishSMSCodeClaim(ctx, phone, SMSRegisterPurpose, "claim-1", true))
	assert.ErrorIs(t, ClaimSMSCode(ctx, phone, SMSRegisterPurpose, code, "claim-3"), ErrSMSCodeInvalid)
}

func TestSMSCodeReleaseAllowsRegistrationRetry(t *testing.T) {
	useSMSVerificationMiniRedis(t)
	ctx := context.Background()
	phone := "+8613800138000"
	code := "123456"

	require.NoError(t, SavePendingSMSCode(ctx, phone, SMSRegisterPurpose, code))
	require.NoError(t, ActivateSMSCode(ctx, phone, SMSRegisterPurpose, code))
	require.NoError(t, ClaimSMSCode(ctx, phone, SMSRegisterPurpose, code, "claim-1"))
	require.NoError(t, FinishSMSCodeClaim(ctx, phone, SMSRegisterPurpose, "claim-1", false))
	require.NoError(t, ClaimSMSCode(ctx, phone, SMSRegisterPurpose, code, "claim-2"))
}

func TestSMSCodeReleasePreservesOriginalExpiry(t *testing.T) {
	server := useSMSVerificationMiniRedis(t)
	ctx := context.Background()
	phone := "+8613800138000"
	code := "123456"

	require.NoError(t, SavePendingSMSCode(ctx, phone, SMSRegisterPurpose, code))
	require.NoError(t, ActivateSMSCode(ctx, phone, SMSRegisterPurpose, code))
	server.FastForward(4 * time.Minute)
	require.NoError(t, ClaimSMSCode(ctx, phone, SMSRegisterPurpose, code, "claim-1"))
	require.NoError(t, FinishSMSCodeClaim(ctx, phone, SMSRegisterPurpose, "claim-1", false))
	server.FastForward(61 * time.Second)
	assert.ErrorIs(t, ClaimSMSCode(ctx, phone, SMSRegisterPurpose, code, "claim-2"), ErrSMSCodeInvalid)
}

func TestSMSCodeExpiresAfterFiveIncorrectAttempts(t *testing.T) {
	useSMSVerificationMiniRedis(t)
	ctx := context.Background()
	phone := "+8613800138000"

	require.NoError(t, SavePendingSMSCode(ctx, phone, SMSRegisterPurpose, "123456"))
	require.NoError(t, ActivateSMSCode(ctx, phone, SMSRegisterPurpose, "123456"))
	for attempt := 0; attempt < 5; attempt++ {
		assert.ErrorIs(t, ClaimSMSCode(ctx, phone, SMSRegisterPurpose, "000000", "claim"), ErrSMSCodeInvalid)
	}
	assert.ErrorIs(t, ClaimSMSCode(ctx, phone, SMSRegisterPurpose, "123456", "claim"), ErrSMSCodeInvalid)
}

func TestSMSVerificationFailsClosedWithoutRedis(t *testing.T) {
	previousRedisEnabled := common.RedisEnabled
	previousRedisClient := common.RDB
	common.RedisEnabled = false
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRedisClient
	})

	err := SavePendingSMSCode(context.Background(), "+8613800138000", SMSRegisterPurpose, "123456")
	assert.ErrorIs(t, err, ErrSMSUnavailable)
}