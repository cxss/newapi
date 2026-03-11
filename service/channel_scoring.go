package service

import (
	"math"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// ChannelScore represents a channel with its calculated score
type ChannelScore struct {
	Channel *model.Channel
	Score   float64
}

// ScoringWeights defines the weights for different factors in channel scoring
type ScoringWeights struct {
	PriceWeight       float64
	LatencyWeight     float64
	FailureRateWeight float64
}

// DefaultScoringWeights returns the default weights with cost as highest priority (70%)
func DefaultScoringWeights() ScoringWeights {
	return ScoringWeights{
		PriceWeight:       0.70,
		LatencyWeight:     0.15,
		FailureRateWeight: 0.15,
	}
}

// isChannelRoutable returns true if the channel should participate in routing.
// Manually/auto disabled channels are always excluded.
// Temp-disabled channels are excluded, UNLESS they are in the probe window
// (just exited cooldown) — those are allowed through as probe candidates.
func isChannelRoutable(channel *model.Channel) bool {
	if channel.Status == common.ChannelStatusManuallyDisabled ||
		channel.Status == common.ChannelStatusAutoDisabled {
		return false
	}
	// Allow probe candidates through even though TempDisabledUntil is set
	if channel.IsTempDisabled() && !channel.IsProbeCandidate() {
		return false
	}
	return true
}

// CalculateChannelScore calculates a composite score for a channel.
// Lower score is better. Channels with success rate < 90% receive a heavy penalty.
func CalculateChannelScore(channel *model.Channel, modelName string, weights ScoringWeights) float64 {
	modelPrice := ratio_setting.GetModelPrice(modelName, false)
	if modelPrice == 0 {
		modelPrice = 1.0
	}
	channelModelRatio := ratio_setting.GetChannelModelRatio(channel.Type, modelName)
	effectivePrice := modelPrice * channelModelRatio

	// Logarithmic price normalization (range 0-100)
	normalizedPrice := math.Log1p(effectivePrice) / math.Log1p(100.0)
	if normalizedPrice > 1.0 {
		normalizedPrice = 1.0
	}

	// Latency normalization (range 0-5000ms)
	latency := float64(channel.GetLatency())
	normalizedLatency := latency / 5000.0
	if normalizedLatency > 1.0 {
		normalizedLatency = 1.0
	}

	successRate := channel.GetSuccessRate()
	failureRate := 1.0 - successRate

	score := weights.PriceWeight*normalizedPrice +
		weights.LatencyWeight*normalizedLatency +
		weights.FailureRateWeight*failureRate

	// Health penalty: success rate < 90% triggers heavy score penalty
	// so the system avoids this channel unless no better option exists
	if successRate < common.ChannelLowSuccessRateThreshold {
		score += common.ChannelLowSuccessRatePenalty * failureRate
	}

	// Probe candidates (just exited cooldown) get a score bump so they are
	// tried last among healthy channels but still ahead of truly unhealthy ones
	if channel.IsProbeCandidate() {
		score += 0.5
	}

	return score
}

// GetScoredChannels filters unroutable channels then scores and sorts the rest.
func GetScoredChannels(channels []*model.Channel, modelName string) []ChannelScore {
	weights := DefaultScoringWeights()
	scored := make([]ChannelScore, 0, len(channels))

	for _, ch := range channels {
		if !isChannelRoutable(ch) {
			continue
		}
		score := CalculateChannelScore(ch, modelName, weights)
		scored = append(scored, ChannelScore{Channel: ch, Score: score})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score < scored[j].Score
	})

	return scored
}

// GetChannelsByScore returns routable channels sorted by score for failover logic.
func GetChannelsByScore(group string, modelName string) ([]*model.Channel, error) {
	channels, err := getAllAvailableChannels(group, modelName)
	if err != nil {
		return nil, err
	}
	if len(channels) == 0 {
		return nil, nil
	}

	scoredChannels := GetScoredChannels(channels, modelName)
	result := make([]*model.Channel, len(scoredChannels))
	for i, sc := range scoredChannels {
		result[i] = sc.Channel
	}
	return result, nil
}

// GetBestChannelByScore returns the single best routable channel.
func GetBestChannelByScore(group string, modelName string) (*model.Channel, error) {
	channels, err := GetChannelsByScore(group, modelName)
	if err != nil {
		return nil, err
	}
	if len(channels) == 0 {
		return nil, nil
	}
	return channels[0], nil
}

func getAllAvailableChannels(group string, modelName string) ([]*model.Channel, error) {
	if common.MemoryCacheEnabled {
		return getAllAvailableChannelsFromCache(group, modelName)
	}
	return getAllAvailableChannelsFromDB(group, modelName)
}

func getAllAvailableChannelsFromCache(group string, modelName string) ([]*model.Channel, error) {
	model.ChannelSyncLock.RLock()
	defer model.ChannelSyncLock.RUnlock()

	channelIDs := model.Group2model2channels[group][modelName]
	if len(channelIDs) == 0 {
		normalizedModel := ratio_setting.FormatMatchingModelName(modelName)
		channelIDs = model.Group2model2channels[group][normalizedModel]
	}
	if len(channelIDs) == 0 {
		return nil, nil
	}

	channels := make([]*model.Channel, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		if ch, ok := model.ChannelsIDM[channelID]; ok {
			channels = append(channels, ch)
		}
	}
	return channels, nil
}

func getAllAvailableChannelsFromDB(group string, modelName string) ([]*model.Channel, error) {
	return model.GetChannelsByGroupAndModel(group, modelName)
}
