# 智能路由系统实现文档

## 概述

本系统实现了基于成本优先的智能路由算法，自动选择最优的渠道来处理 AI 请求。

## 核心特性

### 1. 评分算法

**公式：** `score = W_p × NormalizedPrice + W_l × NormalizedLatency + W_f × FailureRate`

**默认权重：**
- 成本权重 (W_p): 70% - 最高优先级
- 延迟权重 (W_l): 15%
- 失败率权重 (W_f): 15%

**评分越低越好**，系统会优先选择得分最低的渠道。

### 2. 数据结构更新

在 `Channel` 模型中新增三个字段：

```go
type Channel struct {
    // ... 现有字段 ...

    // Smart routing metrics
    Latency       *int     `json:"latency" gorm:"default:0"`        // 平均延迟（毫秒）
    SuccessRate   *float64 `json:"success_rate" gorm:"default:1.0"` // 成功率 (0.0 - 1.0)
    LastCheckTime int64    `json:"last_check_time" gorm:"bigint;default:0"` // 最后检查时间
}
```

### 3. 自动指标更新

系统会在每次请求后自动更新渠道指标：

- **延迟 (Latency)**: 使用指数移动平均 (EMA, α=0.3) 更新
- **成功率 (SuccessRate)**: 使用指数移动平均 (EMA, α=0.2) 更新
- **最后检查时间**: 记录最后一次请求的时间戳

```go
// 指数移动平均公式
newLatency = 0.3 × currentLatency + 0.7 × historicalLatency
newSuccessRate = 0.2 × (success ? 1.0 : 0.0) + 0.8 × historicalSuccessRate
```

### 4. 智能 Failover

当最优渠道失败时，系统会自动尝试下一个得分最低的渠道：

```
请求 → 选择得分最低的渠道 (retry=0)
  ↓ 失败
选择得分第二低的渠道 (retry=1)
  ↓ 失败
选择得分第三低的渠道 (retry=2)
  ↓ ...
```

## 实现细节

### 文件修改清单

1. **model/channel.go**
   - 添加新字段：`Latency`, `SuccessRate`, `LastCheckTime`
   - 添加辅助方法：`GetLatency()`, `GetSuccessRate()`, `UpdateMetrics()`

2. **model/channel_cache.go**
   - 导出变量：`Group2model2channels`, `ChannelsIDM`, `ChannelSyncLock`

3. **model/ability.go**
   - 新增方法：`GetChannelsByGroupAndModel()` - 获取所有可用渠道

4. **service/channel_scoring.go** (新文件)
   - `CalculateChannelScore()` - 计算渠道评分
   - `GetScoredChannels()` - 获取排序后的渠道列表
   - `GetChannelsByScore()` - 按评分排序返回渠道

5. **service/channel_select.go**
   - 修改 `CacheGetRandomSatisfiedChannel()` 使用评分系统
   - 支持基于评分的 failover

6. **controller/relay.go**
   - 在请求前记录开始时间
   - 在请求后更新渠道指标（成功/失败）

## 使用示例

### 查看渠道评分

```go
import "github.com/QuantumNous/new-api/service"

// 获取某个分组和模型的所有渠道（按评分排序）
channels, err := service.GetChannelsByScore("default", "gpt-4o")
if err != nil {
    // 处理错误
}

// 第一个渠道是得分最低（最优）的
bestChannel := channels[0]
```

### 自定义评分权重

```go
import "github.com/QuantumNous/new-api/service"

// 创建自定义权重
weights := service.ScoringWeights{
    PriceWeight:       0.80, // 80% 成本权重
    LatencyWeight:     0.10, // 10% 延迟权重
    FailureRateWeight: 0.10, // 10% 失败率权重
}

// 计算单个渠道的评分
score := service.CalculateChannelScore(channel, "gpt-4o", weights)
```

## 数据库迁移

新字段会在系统启动时自动添加（通过 GORM AutoMigrate）：

```sql
-- 自动执行的迁移（示例）
ALTER TABLE channels ADD COLUMN latency INT DEFAULT 0;
ALTER TABLE channels ADD COLUMN success_rate DOUBLE DEFAULT 1.0;
ALTER TABLE channels ADD COLUMN last_check_time BIGINT DEFAULT 0;
```

## 监控与调优

### 查看渠道指标

```sql
SELECT
    id,
    name,
    latency,
    success_rate,
    last_check_time,
    FROM_UNIXTIME(last_check_time) as last_check_datetime
FROM channels
WHERE status = 1
ORDER BY latency ASC;
```

### 调优建议

1. **成本敏感场景**：保持默认权重 (70% 成本)
2. **性能敏感场景**：增加延迟权重到 40-50%
3. **稳定性优先**：增加失败率权重到 30-40%

## 向后兼容

- 新字段有默认值，不影响现有渠道
- 旧的优先级/权重系统已被评分系统替代
- 如需回退，可以修改 `service/channel_select.go` 恢复旧逻辑

## 性能考虑

1. **缓存优先**：优先使用内存缓存 (`MemoryCacheEnabled`)
2. **异步更新**：指标更新使用 goroutine 异步执行
3. **批量查询**：从数据库获取渠道时使用 `IN` 查询

## 故障排查

### 问题：渠道评分不更新

**检查：**
1. 确认 `UpdateMetrics()` 被调用
2. 检查数据库连接
3. 查看日志中的错误信息

### 问题：总是选择同一个渠道

**原因：** 该渠道评分最低（成本最优）

**解决：**
1. 调整评分权重，增加延迟或失败率的权重
2. 检查其他渠道的价格配置
3. 确认其他渠道的 `success_rate` 是否正常

## 未来扩展

可以考虑添加的功能：

1. **地理位置权重**：根据用户地理位置选择最近的渠道
2. **时间段权重**：在不同时间段使用不同的权重策略
3. **负载均衡**：考虑渠道当前负载情况
4. **成本预算**：设置每日/每月成本上限
5. **A/B 测试**：对比不同评分策略的效果
