package service

import (
	"context"
	"fmt"
	"time"

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

	// Step B: calculate raw service fee for this request
	rawFee := int64(float64(providerCostQuota) * settings.PlatformServiceFeeRate)
	if rawFee <= 0 {
		// No fee to charge (free model or zero cost)
		return result, nil
	}

	cap := settings.MonthlyServiceFeeCap

	err := model.DB.Transaction(func(tx *gorm.DB) error {
		// Lock the user row for update
		var user model.User
		if err := tx.Set("gorm:query_option", "FOR UPDATE").
			Select("id, month_fee_total, last_fee_reset_time, quota").
			Where("id = ?", userId).
			First(&user).Error; err != nil {
			return err
		}

		// Step A: monthly reset check
		now := time.Now()
		lastReset := time.Unix(user.LastFeeResetTime, 0)
		if now.Year() != lastReset.Year() || now.Month() != lastReset.Month() {
			user.MonthFeeTotal = 0
			user.LastFeeResetTime = now.Unix()
		}

		// Step C: cap enforcement
		var actualFee int64
		if user.MonthFeeTotal >= cap {
			// Already at cap — no fee
			result.CapReached = true
			actualFee = 0
		} else if user.MonthFeeTotal+rawFee > cap {
			// Would exceed cap — charge only the remainder
			actualFee = cap - user.MonthFeeTotal
			result.CapPartial = true
		} else {
			actualFee = rawFee
		}

		result.ServiceFee = actualFee

		if actualFee > 0 {
			// Step D: atomic deduct quota + update monthly accumulator
			if err := tx.Model(&model.User{}).
				Where("id = ?", userId).
				Updates(map[string]interface{}{
					"quota":              gorm.Expr("quota - ?", actualFee),
					"month_fee_total":    gorm.Expr("month_fee_total + ?", actualFee),
					"last_fee_reset_time": user.LastFeeResetTime,
				}).Error; err != nil {
				return err
			}
		} else if user.LastFeeResetTime != 0 {
			// Still persist the reset timestamp even if no fee charged
			if err := tx.Model(&model.User{}).
				Where("id = ?", userId).
				Updates(map[string]interface{}{
					"month_fee_total":    user.MonthFeeTotal,
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

	// Log the breakdown
	capStatus := "no"
	if result.CapReached {
		capStatus = "cap_reached"
	} else if result.CapPartial {
		capStatus = "cap_partial"
	}
	logger.LogInfo(ctx, fmt.Sprintf(
		"service_fee: provider_cost=%d service_fee=%d cap_status=%s",
		result.ProviderCost, result.ServiceFee, capStatus,
	))

	return result, nil
}
