package service

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

// ServiceFeeResult holds the breakdown of a single billing settlement.
type ServiceFeeResult struct {
	ProviderCost int64 // raw provider cost in Quota units
	ServiceFee   int64 // platform service fee charged this request
	CapReached   bool  // true if monthly cap was already hit before this request
	CapPartial   bool  // true if cap was hit mid-request (only partial fee charged)
}

// ChargeServiceFee calculates and atomically deducts the platform service fee
// for a single request. It handles monthly reset, cap enforcement, and DB update
// in a single transaction to ensure consistency under concurrency.
//
// providerCostQuota is the raw provider cost for this request in Quota units.
// Returns a ServiceFeeResult describing what was charged.
func ChargeServiceFee(ctx context.Context, userId int, providerCostQuota int64) (ServiceFeeResult, error) {
	settings := operation_setting.GetServiceFeeSetting()
	result := ServiceFeeResult{ProviderCost: providerCostQuota}

	// Step B: calculate raw service fee using Ceil to avoid truncation to zero
	// on small requests (e.g. 5 quota * 0.01 = 0.05 → ceil → 1)
	rawFee := int64(math.Ceil(float64(providerCostQuota) * settings.PlatformServiceFeeRate))
	if rawFee <= 0 {
		return result, nil
	}

	cap := settings.MonthlyServiceFeeCap

	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Set("gorm:query_option", "FOR UPDATE").
			Select("id, month_fee_total, last_fee_reset_time, quota").
			Where("id = ?", userId).
			First(&user).Error; err != nil {
			return err
		}

		// Step A: monthly reset — compare Year+Month of now vs last reset
		now := time.Now()
		lastReset := time.Unix(user.LastFeeResetTime, 0)
		if now.Year() != lastReset.Year() || now.Month() != lastReset.Month() {
			user.MonthFeeTotal = 0
			user.LastFeeResetTime = now.Unix()
		}

		// Step C: cap enforcement
		var actualFee int64
		if user.MonthFeeTotal >= cap {
			result.CapReached = true
			actualFee = 0
		} else if user.MonthFeeTotal+rawFee > cap {
			actualFee = cap - user.MonthFeeTotal
			result.CapPartial = true
		} else {
			actualFee = rawFee
		}

		result.ServiceFee = actualFee

		// Step D: atomic quota deduction + accumulator update
		if actualFee > 0 {
			if err := tx.Model(&model.User{}).
				Where("id = ?", userId).
				Updates(map[string]interface{}{
					"quota":               gorm.Expr("quota - ?", actualFee),
					"month_fee_total":     gorm.Expr("month_fee_total + ?", actualFee),
					"last_fee_reset_time": user.LastFeeResetTime,
				}).Error; err != nil {
				return err
			}
		} else {
			// Persist reset timestamp even when no fee is charged
			if err := tx.Model(&model.User{}).
				Where("id = ?", userId).
				Updates(map[string]interface{}{
					"month_fee_total":     user.MonthFeeTotal,
					"last_fee_reset_time": user.LastFeeResetTime,
				}).Error; err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return result, fmt.Errorf("service fee transaction failed: %w", err)
	}

	// Step E: record service fee as a separate log entry with both fields explicit
	// so billing UI can display provider_cost and service_fee independently
	capStatus := "no"
	if result.CapReached {
		capStatus = "cap_reached"
	} else if result.CapPartial {
		capStatus = "cap_partial"
	}

	logOther := map[string]interface{}{
		"provider_cost": result.ProviderCost,
		"service_fee":   result.ServiceFee,
		"cap_status":    capStatus,
		"log_type":      "service_fee",
	}
	model.RecordLog(userId, model.LogTypeConsume, common.MapToJsonStr(logOther))

	logger.LogInfo(ctx, fmt.Sprintf(
		"service_fee: provider_cost=%d service_fee=%d cap_status=%s",
		result.ProviderCost, result.ServiceFee, capStatus,
	))

	return result, nil
}
