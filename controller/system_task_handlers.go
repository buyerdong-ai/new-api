package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
)

// RegisterScheduledSystemTasks wires the periodic channel test, upstream model
// update, and async task polling (Midjourney / Suno / video) jobs into the
// system task framework so a DB lease dedups execution across multiple master
// instances and each run is recorded as one task row. Call this before
// service.StartSystemTaskRunner.
func RegisterScheduledSystemTasks() {
	service.RegisterSystemTaskHandler(channelTestHandler{})
	service.RegisterSystemTaskHandler(modelUpdateHandler{})
	service.RegisterSystemTaskHandler(midjourneyPollHandler{})
	service.RegisterSystemTaskHandler(asyncTaskPollHandler{})
	service.RegisterSystemTaskHandler(weChatPaySyncHandler{})
	service.RegisterSystemTaskHandler(weChatPayReconcileHandler{})
}

// channelTestHandler runs the scheduled "test all channels" job. Enablement and
// cadence still come from the monitor settings; only the execution path moved
// into the system task runner.
type channelTestHandler struct{}

func (channelTestHandler) Type() string { return model.SystemTaskTypeChannelTest }

func (channelTestHandler) Enabled() bool {
	return operation_setting.GetMonitorSetting().AutoTestChannelEnabled
}

func (channelTestHandler) Interval() time.Duration {
	minutes := operation_setting.GetMonitorSetting().AutoTestChannelMinutes
	if minutes <= 0 {
		minutes = 10
	}
	return time.Duration(minutes * float64(time.Minute))
}

func (channelTestHandler) NewPayload() any { return nil }

// channelTestTaskPayload controls one channel_test run. A nil/empty payload is a
// scheduled run, which uses the configured monitor ChannelTestMode and does not
// notify. A manual "test all channels" trigger sets Mode=scheduled_all and
// Notify=true to reproduce the legacy manual behavior (test every channel and
// notify root on completion).
type channelTestTaskPayload struct {
	Mode   string `json:"mode,omitempty"`
	Notify bool   `json:"notify,omitempty"`
}

func (channelTestHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	payload := channelTestTaskPayload{}
	if err := task.DecodePayload(&payload); err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	summary, err := runChannelTestTask(ctx, payload.Mode, payload.Notify, service.NewSystemTaskProgressReporter(task, runnerID))
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

// modelUpdateHandler runs the scheduled upstream model update detection job.
type modelUpdateHandler struct{}

func (modelUpdateHandler) Type() string { return model.SystemTaskTypeModelUpdate }

func (modelUpdateHandler) Enabled() bool {
	return common.GetEnvOrDefaultBool("CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_ENABLED", true)
}

func (modelUpdateHandler) Interval() time.Duration {
	intervalMinutes := common.GetEnvOrDefault(
		"CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_INTERVAL_MINUTES",
		channelUpstreamModelUpdateTaskDefaultIntervalMinutes,
	)
	if intervalMinutes < 1 {
		intervalMinutes = channelUpstreamModelUpdateTaskDefaultIntervalMinutes
	}
	return time.Duration(intervalMinutes) * time.Minute
}

func (modelUpdateHandler) NewPayload() any { return nil }

// modelUpdateTaskPayload controls one model_update run. A scheduled run
// (Manual=false) respects the per-channel minimum check interval and may
// auto-apply detected models when a channel has auto-sync enabled. A manual
// "detect all" trigger sets Manual=true to reproduce the legacy detect-all
// semantics: force a re-check regardless of the interval and never auto-apply,
// so the admin reviews and applies changes explicitly.
type modelUpdateTaskPayload struct {
	Manual bool `json:"manual,omitempty"`
}

func (modelUpdateHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	payload := modelUpdateTaskPayload{}
	if err := task.DecodePayload(&payload); err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	summary := runChannelUpstreamModelUpdateTaskOnce(ctx, payload.Manual, !payload.Manual, service.NewSystemTaskProgressReporter(task, runnerID))
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

// midjourneyPollHandler runs one Midjourney polling pass per scheduled run.
// Enabled() folds the "are there unfinished tasks?" check into enablement so the
// scheduler creates no row when the system is idle; only when at least one
// Midjourney task is in progress does a row get scheduled.
type midjourneyPollHandler struct{}

func (midjourneyPollHandler) Type() string { return model.SystemTaskTypeMidjourneyPoll }

func (midjourneyPollHandler) Enabled() bool {
	return constant.UpdateTask && model.HasUnfinishedMidjourneyTasks()
}

func (midjourneyPollHandler) Interval() time.Duration { return 15 * time.Second }

func (midjourneyPollHandler) NewPayload() any { return nil }

func (midjourneyPollHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	summary := runMidjourneyTaskUpdateOnce(ctx, service.NewSystemTaskProgressReporter(task, runnerID))
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

// asyncTaskPollHandler runs one async-task (Suno/video) polling pass per
// scheduled run. Like midjourneyPollHandler, Enabled() folds in the unfinished
// task existence check so an idle system schedules no rows.
type asyncTaskPollHandler struct{}

func (asyncTaskPollHandler) Type() string { return model.SystemTaskTypeAsyncTaskPoll }

func (asyncTaskPollHandler) Enabled() bool {
	return constant.UpdateTask && model.HasUnfinishedSyncTasks()
}

func (asyncTaskPollHandler) Interval() time.Duration { return 15 * time.Second }

func (asyncTaskPollHandler) NewPayload() any { return nil }

func (asyncTaskPollHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	summary := service.RunTaskPollingOnce(ctx, service.NewSystemTaskProgressReporter(task, runnerID))
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

type weChatPaySyncHandler struct{}

type weChatPaySyncSummary struct {
	Scanned                  int     `json:"scanned"`
	Completed                int     `json:"completed"`
	Pending                  int     `json:"pending"`
	Failures                 int     `json:"failures"`
	FailureRate              float64 `json:"failure_rate"`
	BacklogCount             int64   `json:"backlog_count"`
	OldestOrderAgeSeconds    int64   `json:"oldest_order_age_seconds"`
	CertificateExpiresAt     int64   `json:"certificate_expires_at"`
	CertificateDaysRemaining int     `json:"certificate_days_remaining"`
}

func (weChatPaySyncHandler) Type() string { return model.SystemTaskTypeWeChatPaySync }

func (weChatPaySyncHandler) Enabled() bool { return isWeChatPayTopUpEnabled() }

func (weChatPaySyncHandler) Interval() time.Duration { return time.Minute }

func (weChatPaySyncHandler) NewPayload() any { return nil }

func (weChatPaySyncHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	now := common.GetTimestamp()
	summary := weChatPaySyncSummary{}
	backlog, err := model.GetPendingWeChatTopUpBacklog()
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	summary.BacklogCount = backlog.Count
	if backlog.OldestCreateAt > 0 {
		summary.OldestOrderAgeSeconds = now - backlog.OldestCreateAt
	}
	if summary.BacklogCount >= 100 || summary.OldestOrderAgeSeconds >= int64((30*time.Minute)/time.Second) {
		service.NotifyRootUser("wechat_pay_pending_backlog", "微信支付待支付订单积压", fmt.Sprintf("当前待支付订单 %d 笔，最老订单已等待 %d 分钟。", summary.BacklogCount, summary.OldestOrderAgeSeconds/60))
	}

	if strings.TrimSpace(setting.WeChatPayPublicKeyID) == "" || strings.TrimSpace(setting.WeChatPayPublicKey) == "" {
		certificate, err := utils.LoadCertificate(setting.WeChatPayPlatformCertificate)
		if err != nil {
			finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, summary, fmt.Errorf("load WeChat Pay platform certificate: %w", err))
			return
		}
		summary.CertificateExpiresAt = certificate.NotAfter.Unix()
		summary.CertificateDaysRemaining = int(time.Until(certificate.NotAfter).Hours() / 24)
		if summary.CertificateDaysRemaining <= 30 {
			alertType := "wechat_pay_certificate_expiring"
			title := "微信支付平台证书即将到期"
			if summary.CertificateDaysRemaining <= 7 {
				alertType = "wechat_pay_certificate_expiring_critical"
				title = "微信支付平台证书即将到期（严重）"
			}
			service.NotifyRootUser(alertType, title, fmt.Sprintf("平台证书序列号 %s，将于 %s 到期，剩余 %d 天。", certificate.SerialNumber.Text(16), certificate.NotAfter.Format(time.RFC3339), summary.CertificateDaysRemaining))
		}
	}

	topUps, err := model.FindPendingWeChatTopUps(now-2*60, now-5*60, 100)
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}

	summary.Scanned = len(topUps)
	for _, topUp := range topUps {
		if err := ctx.Err(); err != nil {
			finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, summary, err)
			return
		}
		if err := queryAndCompleteWeChatTopUp(ctx, topUp.TradeNo, "wechat-pay-sync"); err != nil {
			if errors.Is(err, errWeChatOrderPending) {
				summary.Pending++
			} else {
				summary.Failures++
			}
			logger.LogWarn(ctx, fmt.Sprintf("微信支付 主动查单未结算 trade_no=%s error=%q", topUp.TradeNo, err.Error()))
			continue
		}
		summary.Completed++
	}
	if summary.Scanned > 0 {
		summary.FailureRate = float64(summary.Failures) / float64(summary.Scanned)
	}
	if summary.Scanned >= 10 && summary.FailureRate >= 0.2 {
		service.NotifyRootUser("wechat_pay_query_failure_rate", "微信支付主动查单失败率过高", fmt.Sprintf("本轮主动查单 %d 笔，失败 %d 笔，失败率 %.1f%%。", summary.Scanned, summary.Failures, summary.FailureRate*100))
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

type weChatPayReconcileHandler struct{}

func (weChatPayReconcileHandler) Type() string { return model.SystemTaskTypeWeChatPayReconcile }

func (weChatPayReconcileHandler) Enabled() bool { return isWeChatPayTopUpEnabled() }

func (weChatPayReconcileHandler) Interval() time.Duration { return time.Hour }

func (weChatPayReconcileHandler) NewPayload() any { return nil }

func (weChatPayReconcileHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	billDate := time.Now().In(location).AddDate(0, 0, -1).Format("2006-01-02")
	reconciliation, err := runWeChatPayReconciliation(ctx, billDate)
	if err != nil {
		service.NotifyRootUser("wechat_pay_reconciliation_failure", "微信支付每日对账失败", fmt.Sprintf("账单日期 %s 对账失败：%s", billDate, err.Error()))
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, reconciliation, err)
		return
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, reconciliation, nil)
}

func finishSystemTaskHandler(task *model.SystemTask, runnerID string, status model.SystemTaskStatus, result any, runErr error) {
	errorMessage := ""
	if runErr != nil {
		errorMessage = runErr.Error()
	}
	if err := model.FinishSystemTask(task.TaskID, runnerID, status, result, errorMessage); err != nil {
		common.SysLog(fmt.Sprintf("system task %s failed to persist result: %v", task.TaskID, err))
	}
}
