package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

// ServiceFeeSetting 平台服务费配置
type ServiceFeeSetting struct {
	// PlatformServiceFeeRate 服务费率（默认 0.01 = 1%）
	PlatformServiceFeeRate float64 `json:"platform_service_fee_rate"`
	// MonthlyServiceFeeCap 月度服务费封顶金额（单位：Quota，默认 100 * QuotaPerUnit）
	// 使用 int64 以匹配 Quota 精度
	MonthlyServiceFeeCap int64 `json:"monthly_service_fee_cap"`
}

var serviceFeeSetting = ServiceFeeSetting{
	PlatformServiceFeeRate: 0.01,
	MonthlyServiceFeeCap:   500000, // 对应约 $0.5 或按站点汇率折算
}

func init() {
	config.GlobalConfig.Register("service_fee_setting", &serviceFeeSetting)
}

func GetServiceFeeSetting() *ServiceFeeSetting {
	return &serviceFeeSetting
}
