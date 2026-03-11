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
	PriceWeight       float64 // Weight for price (cost)
	LatencyWeight     float64 // Weight for latency
	FailureRateWeight float64 // Weight for failure rate
}

// DefaultScoringWeights returns the default weights with cost as highest priority (70%)
func DefaultScoringWeights() ScoringWeights {
	return ScoringWeights{
		PriceWeight:       0.70, // 70% - Cost is the highest priority
		LatencyWeight:     0.15, // 15% - Latency is secondary
		FailureRateWeight: 0.15, // 15% - Failure rate is tertiary
	}
}

// CalculateChannelScore calculates a composite score for a channel
// Lower score is better (represents lower cost and better performance)
// Formula: score = W_p × NormalizedPrice + W_l × NormalizedLatency + W_f × FailureRate
func CalculateChannelScore(channel *model.Channel, modelName string, weights ScoringWeights) float64 {
	// Get model price (quota per 1000 tokens)
	modelPrice := ratio_setting.GetModelPrice(modelName, false)
	if modelPrice == 0 {
		modelPrice = 1.0 // Default price if not found
	}

	// Apply channel's model ratio if exists
	channelModelRatio := ratio_setting.GetChannelModelRatio(channel.Type, modelName)
	effectivePrice := modelPrice * channelModelRatio

	// Normalize price (assuming typical range 0-100 quota per 1000 tokens)
	// Use logarithmic scale to handle wide price ranges
	normalizedPrice := math.Log1p(effectivePrice) / math.Log1p(100.0)
	if normalizedPrice > 1.0 {
		normalizedPrice = 1.0
	}

	// Get and normalize latency (assuming typical range 0-5000ms)
	latency := float64(channel.GetLatency())
	normalizedLatency := latency / 5000.0
	if normalizedLatency > 1.0 {
		normalizedLatency = 1.0
	}

	// Get failure rate (1.0 - success_rate)
	successRate := channel.GetSuccessRate()
	failureRate := 1.0 - successRate

	// Calculate composite score
	score := weights.PriceWeight*normalizedPrice +
		weights.LatencyWeight*normalizedLatency +
		weights.FailureRateWeight*failureRate

	return score
}

// GetScoredChannels returns channels sorted by score (lowest/best first)
func GetScoredChannels(channels []*model.Channel, modelName string) []ChannelScore {
	weights := DefaultScoringWeights()
	scored := make([]ChannelScore, 0, len(channels))

	for _, channel := range channels {
		score := CalculateChannelScore(channel, modelName, weights)
		scored = append(scored, ChannelScore{
			Channel: channel,
			Score:   score,
		})
	}

	// Sort by score (ascending - lower is better)
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score < scored[j].Score
	})

	return scored
}

// GetBestChannelByScore returns the channel with the lowest score (best cost/performance)
func GetBestChannelByScore(group string, modelName string) (*model.Channel, error) {
	// Get all available channels for this group and model
	channels, err := getAllAvailableChannels(group, modelName)
	if err != nil {
		return nil, err
	}

	if len(channels) == 0 {
		return nil, nil
	}

	// Score and sort channels
	scoredChannels := GetScoredChannels(channels, modelName)

	// Return the best (lowest score) channel
	return scoredChannels[0].Channel, nil
}

// GetChannelsByScore returns all channels sorted by score for retry logic
func GetChannelsByScore(group string, modelName string) ([]*model.Channel, error) {
	// Get all available channels for this group and model
	channels, err := getAllAvailableChannels(group, modelName)
	if err != nil {
		return nil, err
	}

	if len(channels) == 0 {
		return nil, nil
	}

	// Score and sort channels
	scoredChannels := GetScoredChannels(channels, modelName)

	// Extract channels in score order
	result := make([]*model.Channel, len(scoredChannels))
	for i, sc := range scoredChannels {
		result[i] = sc.Channel
	}

	return result, nil
}

// getAllAvailableChannels retrieves all enabled channels for a group and model
func getAllAvailableChannels(group string, modelName string) ([]*model.Channel, error) {
	if common.MemoryCacheEnabled {
		return getAllAvailableChannelsFromCache(group, modelName)
	}
	return getAllAvailableChannelsFromDB(group, modelName)
}

// getAllAvailableChannelsFromCache gets channels from memory cache
func getAllAvailableChannelsFromCache(group string, modelName string) ([]*model.Channel, error) {
	model.ChannelSyncLock.RLock()
	defer model.ChannelSyncLock.RUnlock()

	// Try exact model name first
	channelIDs := model.Group2model2channels[group][modelName]

	// If not found, try normalized model name
	if len(channelIDs) == 0 {
		normalizedModel := ratio_setting.FormatMatchingModelName(modelName)
		channelIDs = model.Group2model2channels[group][normalizedModel]
	}

	if len(channelIDs) == 0 {
		return nil, nil
	}

	// Collect all channels
	channels := make([]*model.Channel, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		if channel, ok := model.ChannelsIDM[channelID]; ok {
			channels = append(channels, channel)
		}
	}

	return channels, nil
}

// getAllAvailableChannelsFromDB gets channels from database
func getAllAvailableChannelsFromDB(group string, modelName string) ([]*model.Channel, error) {
	return model.GetChannelsByGroupAndModel(group, modelName)
}
