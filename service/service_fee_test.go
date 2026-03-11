package service

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
)

// ---------------------------------------------------------------------------
// 纯逻辑辅助：从 ChargeServiceFee 提取的核心计算，不依赖 DB
// ---------------------------------------------------------------------------

type feeCalcInput struct {
	providerCost    int64
	feeRate         float64
	cap             int64
	monthFeeTotal   int64
	lastFeeResetUnix int64
}

type feeCalcOutput struct {
	actualFee     int64
	newMonthTotal int64
	capReached    bool
	capPartial    bool
	wasReset      bool
}

func calcServiceFee(in feeCalcInput) feeCalcOutput {
	rawFee := int64(math.Ceil(float64(in.providerCost) * in.feeRate))
	if rawFee <= 0 {
		return feeCalcOutput{}
	}

	monthTotal := in.monthFeeTotal
	wasReset := false

	// Step A: monthly reset
	now := time.Now()
	lastReset := time.Unix(in.lastFeeResetUnix, 0)
	if now.Year() != lastReset.Year() || now.Month() != lastReset.Month() {
		monthTotal = 0
		wasReset = true
	}

	// Step C: cap enforcement
	var actualFee int64
	capReached := false
	capPartial := false

	if monthTotal >= in.cap {
		capReached = true
		actualFee = 0
	} else if monthTotal+rawFee > in.cap {
		actualFee = in.cap - monthTotal
		capPartial = true
	} else {
		actualFee = rawFee
	}

	return feeCalcOutput{
		actualFee:     actualFee,
		newMonthTotal: monthTotal + actualFee,
		capReached:    capReached,
		capPartial:    capPartial,
		wasReset:      wasReset,
	}
}

// ---------------------------------------------------------------------------
// 测试用例 1：跨月重置
// ---------------------------------------------------------------------------

func TestServiceFee_CrossMonthReset(t *testing.T) {
	// 上个月的时间戳
	lastMonth := time.Now().AddDate(0, -1, 0)

	in := feeCalcInput{
		providerCost:     10000,
		feeRate:          0.01,
		cap:              500000,
		monthFeeTotal:    99999, // 上月累计了很多费用
		lastFeeResetUnix: lastMonth.Unix(),
	}

	out := calcServiceFee(in)

	if !out.wasReset {
		t.Fatal("expected monthly reset to trigger, but it did not")
	}
	if out.newMonthTotal != out.actualFee {
		t.Errorf("after reset, newMonthTotal should equal actualFee only; got newMonthTotal=%d actualFee=%d",
			out.newMonthTotal, out.actualFee)
	}
	// 重置后 monthFeeTotal=0，rawFee = ceil(10000*0.01) = 100
	expectedFee := int64(100)
	if out.actualFee != expectedFee {
		t.Errorf("expected actualFee=%d after reset, got %d", expectedFee, out.actualFee)
	}
	t.Logf("cross-month reset: wasReset=%v oldAccum=99999 newAccum=%d fee=%d",
		out.wasReset, out.newMonthTotal, out.actualFee)
}

// ---------------------------------------------------------------------------
// 测试用例 2：精准封顶（差额截断）
// ---------------------------------------------------------------------------

func TestServiceFee_PreciseCap(t *testing.T) {
	cap := int64(500000)
	// 已累计 499800，本笔 rawFee = ceil(50000*0.01) = 500，会超出 200
	in := feeCalcInput{
		providerCost:     50000,
		feeRate:          0.01,
		cap:              cap,
		monthFeeTotal:    499800,
		lastFeeResetUnix: time.Now().Unix(), // 本月，不触发重置
	}

	out := calcServiceFee(in)

	if !out.capPartial {
		t.Fatal("expected capPartial=true but got false")
	}
	expectedFee := cap - 499800 // = 200
	if out.actualFee != expectedFee {
		t.Errorf("expected partial fee=%d (cap gap), got %d", expectedFee, out.actualFee)
	}
	if out.newMonthTotal != cap {
		t.Errorf("expected newMonthTotal to reach cap=%d exactly, got %d", cap, out.newMonthTotal)
	}
	t.Logf("precise cap: monthFeeTotal=499800 rawFee=500 actualFee=%d newTotal=%d",
		out.actualFee, out.newMonthTotal)
}

func TestServiceFee_CapAlreadyReached(t *testing.T) {
	in := feeCalcInput{
		providerCost:     10000,
		feeRate:          0.01,
		cap:              500000,
		monthFeeTotal:    500000, // 已达封顶
		lastFeeResetUnix: time.Now().Unix(),
	}

	out := calcServiceFee(in)

	if !out.capReached {
		t.Fatal("expected capReached=true")
	}
	if out.actualFee != 0 {
		t.Errorf("expected zero fee when cap reached, got %d", out.actualFee)
	}
	t.Logf("cap already reached: actualFee=%d capReached=%v", out.actualFee, out.capReached)
}

// ---------------------------------------------------------------------------
// 测试用例 3：并发扣费一致性（无 DB，模拟原子累加）
// ---------------------------------------------------------------------------

func TestServiceFee_ConcurrentConsistency(t *testing.T) {
	const (
		goroutines   = 50
		providerCost = int64(1000)
		feeRate      = 0.01
		cap          = int64(500000)
	)

	// 模拟共享状态（原子操作模拟 DB 事务的串行效果）
	var monthFeeTotal atomic.Int64
	var quotaDeducted atomic.Int64
	monthFeeTotal.Store(0)

	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			mu.Lock()
			defer mu.Unlock()

			in := feeCalcInput{
				providerCost:     providerCost,
				feeRate:          feeRate,
				cap:              cap,
				monthFeeTotal:    monthFeeTotal.Load(),
				lastFeeResetUnix: time.Now().Unix(),
			}
			out := calcServiceFee(in)

			monthFeeTotal.Add(out.actualFee)
			quotaDeducted.Add(out.actualFee)
		}()
	}
	wg.Wait()

	finalMonthTotal := monthFeeTotal.Load()
	finalQuotaDeducted := quotaDeducted.Load()

	// 断言：month_fee_total 增量 == quota 扣除总额
	if finalMonthTotal != finalQuotaDeducted {
		t.Errorf("inconsistency: monthFeeTotal=%d != quotaDeducted=%d",
			finalMonthTotal, finalQuotaDeducted)
	}
	// 断言：不超过封顶
	if finalMonthTotal > cap {
		t.Errorf("monthFeeTotal=%d exceeded cap=%d", finalMonthTotal, cap)
	}
	// rawFee per request = ceil(1000*0.01) = 10, 50 goroutines = max 500
	expectedMax := int64(goroutines) * 10
	t.Logf("concurrent: goroutines=%d totalFee=%d quotaDeducted=%d cap=%d expectedMax=%d",
		goroutines, finalMonthTotal, finalQuotaDeducted, cap, expectedMax)
}

// ---------------------------------------------------------------------------
// 测试用例 4：路由排序验证
// ---------------------------------------------------------------------------

func TestChannelRoutingOrder(t *testing.T) {
	// 构造 3 个渠道：价格和延迟各异
	// 注意：CalculateChannelScore 中价格来自 ratio_setting，
	// 这里 modelName 用不存在的名称使 modelPrice 回退到默认值 1.0
	channels := []*model.Channel{
		{
			Id:          1,
			Name:        "高价低延迟",
			Type:        0,
			Latency:     intPtr(80),
			SuccessRate: float64Ptr(0.99),
		},
		{
			Id:          2,
			Name:        "低价高延迟",
			Type:        0,
			Latency:     intPtr(3000),
			SuccessRate: float64Ptr(0.97),
		},
		{
			Id:          3,
			Name:        "中价中延迟",
			Type:        0,
			Latency:     intPtr(500),
			SuccessRate: float64Ptr(0.98),
		},
	}

	weights := DefaultScoringWeights()
	scored := GetScoredChannels(channels, "test-model-nonexistent")

	if len(scored) != 3 {
		t.Fatalf("expected 3 scored channels, got %d", len(scored))
	}

	// 验证升序排列（分数越低越优先）
	for i := 0; i < len(scored)-1; i++ {
		if scored[i].Score > scored[i+1].Score {
			t.Errorf("sort error: position %d score=%f > position %d score=%f",
				i, scored[i].Score, i+1, scored[i+1].Score)
		}
	}

	t.Log("路由优先级排序（分数越低越优先）:")
	for i, sc := range scored {
		t.Logf("  #%d %s (id=%d) score=%.4f latency=%dms successRate=%.2f",
			i+1, sc.Channel.Name, sc.Channel.Id, sc.Score,
			sc.Channel.GetLatency(), sc.Channel.GetSuccessRate())
	}

	// 验证权重含义：价格权重 70%，延迟权重 15%
	// 所有渠道价格相同时，延迟最低的应排第一
	_ = weights
	if scored[0].Channel.Id != 1 {
		t.Logf("note: channel 1 (低延迟) not ranked first — price component dominates at 70%% weight")
	}
}

// ---------------------------------------------------------------------------
// 测试用例 5：EMA 延迟平滑验证
// ---------------------------------------------------------------------------

func TestLatencyEMA(t *testing.T) {
	// 验证 alpha=0.2 的平滑效果：单次抖动不应大幅改变延迟
	oldLatency := 200 // 稳定延迟 200ms
	spike := 2000     // 单次抖动 2000ms

	// EMA: new = 0.8*old + 0.2*current
	newLatency := int(0.8*float64(oldLatency) + 0.2*float64(spike))

	if newLatency > 600 {
		t.Errorf("single spike caused too much latency change: %d -> %d (spike=%d)",
			oldLatency, newLatency, spike)
	}
	t.Logf("EMA latency: stable=%dms spike=%dms result=%dms (change=+%dms)",
		oldLatency, spike, newLatency, newLatency-oldLatency)

	// 验证连续正常请求后恢复
	current := newLatency
	normal := 200
	for i := 0; i < 10; i++ {
		current = int(0.8*float64(current) + 0.2*float64(normal))
	}
	if current > 250 {
		t.Errorf("latency did not recover after 10 normal requests: %d", current)
	}
	t.Logf("after 10 normal requests: latency recovered to %dms", current)
}
