package service

import (
	"fmt"

	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const (
	BillingSourceWallet       = "wallet"
	BillingSourceSubscription = "subscription"
)

// PreConsumeBilling 根据用户计费偏好创建 BillingSession 并执行预扣费。
// 会话存储在 relayInfo.Billing 上，供后续 Settle / Refund 使用。
func PreConsumeBilling(c *gin.Context, preConsumedQuota int, relayInfo *relaycommon.RelayInfo) *types.NewAPIError {
	session, apiErr := NewBillingSession(c, relayInfo, preConsumedQuota)
	if apiErr != nil {
		return apiErr
	}
	relayInfo.Billing = session
	return nil
}

// ---------------------------------------------------------------------------
// SettleBilling — 后结算辅助函数
// ---------------------------------------------------------------------------

// SettleBilling 执行计费结算。如果 RelayInfo 上有 BillingSession 则通过 session 结算，
// 否则回退到旧的 PostConsumeQuota 路径（兼容按次计费等场景）。
// 结算完成后，若 actualQuota > 0 且为钱包计费，额外收取平台服务费。
func SettleBilling(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, actualQuota int) error {
	if relayInfo.Billing != nil {
		preConsumed := relayInfo.Billing.GetPreConsumedQuota()
		delta := actualQuota - preConsumed

		if delta > 0 {
			logger.LogInfo(ctx, fmt.Sprintf("预扣费后补扣费：%s（实际消耗：%s，预扣费：%s）",
				logger.FormatQuota(delta),
				logger.FormatQuota(actualQuota),
				logger.FormatQuota(preConsumed),
			))
		} else if delta < 0 {
			logger.LogInfo(ctx, fmt.Sprintf("预扣费后返还扣费：%s（实际消耗：%s，预扣费：%s）",
				logger.FormatQuota(-delta),
				logger.FormatQuota(actualQuota),
				logger.FormatQuota(preConsumed),
			))
		} else {
			logger.LogInfo(ctx, fmt.Sprintf("预扣费与实际消耗一致，无需调整：%s（按次计费）",
				logger.FormatQuota(actualQuota),
			))
		}

		if err := relayInfo.Billing.Settle(actualQuota); err != nil {
			return err
		}

		// 发送额度通知（订阅计费使用订阅剩余额度）
		if actualQuota != 0 {
			if relayInfo.BillingSource == BillingSourceSubscription {
				checkAndSendSubscriptionQuotaNotify(relayInfo)
			} else {
				checkAndSendQuotaNotify(relayInfo, actualQuota-preConsumed, preConsumed)
			}
		}
	} else {
		// 回退：无 BillingSession 时使用旧路径
		quotaDelta := actualQuota - relayInfo.FinalPreConsumedQuota
		if quotaDelta != 0 {
			if err := PostConsumeQuota(relayInfo, quotaDelta, relayInfo.FinalPreConsumedQuota, true); err != nil {
				return err
			}
		}
	}

	// 平台服务费：仅对钱包计费且有实际消耗时收取
	if actualQuota > 0 && relayInfo.BillingSource != BillingSourceSubscription && relayInfo.UserId > 0 {
		feeResult, err := ChargeServiceFee(ctx, relayInfo.UserId, int64(actualQuota))
		if err != nil {
			// 服务费失败不阻断主流程，仅记录错误
			logger.LogError(ctx, fmt.Sprintf("service fee charge failed: %s", err.Error()))
		} else {
			capNote := ""
			if feeResult.CapReached {
				capNote = "（已达月度封顶，免收）"
			} else if feeResult.CapPartial {
				capNote = "（触发月度封顶，仅收差额）"
			}
			logger.LogInfo(ctx, fmt.Sprintf(
				"平台服务费：原始成本=%s 本笔服务费=%s%s",
				logger.FormatQuota(int(feeResult.ProviderCost)),
				logger.FormatQuota(int(feeResult.ServiceFee)),
				capNote,
			))
		}
	}

	return nil
}
