package service

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
)

func TestCalculateChannelScore(t *testing.T) {
	// Create test channels with different characteristics
	tests := []struct {
		name        string
		channel     *model.Channel
		modelName   string
		expectedMin float64
		expectedMax float64
	}{
		{
			name: "Low latency, high success rate",
			channel: &model.Channel{
				Id:          1,
				Name:        "Fast Channel",
				Latency:     intPtr(100),
				SuccessRate: float64Ptr(0.99),
			},
			modelName:   "gpt-4o",
			expectedMin: 0.0,
			expectedMax: 0.5,
		},
		{
			name: "High latency, low success rate",
			channel: &model.Channel{
				Id:          2,
				Name:        "Slow Channel",
				Latency:     intPtr(5000),
				SuccessRate: float64Ptr(0.80),
			},
			modelName:   "gpt-4o",
			expectedMin: 0.3,
			expectedMax: 1.0,
		},
		{
			name: "Default values (new channel)",
			channel: &model.Channel{
				Id:          3,
				Name:        "New Channel",
				Latency:     nil,
				SuccessRate: nil,
			},
			modelName:   "gpt-4o",
			expectedMin: 0.0,
			expectedMax: 1.0,
		},
	}

	weights := DefaultScoringWeights()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := CalculateChannelScore(tt.channel, tt.modelName, weights)

			if score < tt.expectedMin || score > tt.expectedMax {
				t.Errorf("Score %f is outside expected range [%f, %f]", score, tt.expectedMin, tt.expectedMax)
			}

			t.Logf("Channel: %s, Score: %f", tt.channel.Name, score)
		})
	}
}

func TestGetScoredChannels(t *testing.T) {
	// Create test channels
	channels := []*model.Channel{
		{
			Id:          1,
			Name:        "Expensive but fast",
			Latency:     intPtr(50),
			SuccessRate: float64Ptr(0.99),
		},
		{
			Id:          2,
			Name:        "Cheap but slow",
			Latency:     intPtr(2000),
			SuccessRate: float64Ptr(0.95),
		},
		{
			Id:          3,
			Name:        "Balanced",
			Latency:     intPtr(500),
			SuccessRate: float64Ptr(0.97),
		},
	}

	scored := GetScoredChannels(channels, "gpt-4o")

	// Verify channels are sorted by score (ascending)
	for i := 0; i < len(scored)-1; i++ {
		if scored[i].Score > scored[i+1].Score {
			t.Errorf("Channels not sorted correctly: %f > %f", scored[i].Score, scored[i+1].Score)
		}
	}

	// Log results
	t.Log("Scored channels (best to worst):")
	for i, sc := range scored {
		t.Logf("%d. %s (ID: %d, Score: %f)", i+1, sc.Channel.Name, sc.Channel.Id, sc.Score)
	}
}

func TestDefaultScoringWeights(t *testing.T) {
	weights := DefaultScoringWeights()

	// Verify cost weight is highest (70%)
	if weights.PriceWeight != 0.70 {
		t.Errorf("Expected PriceWeight to be 0.70, got %f", weights.PriceWeight)
	}

	// Verify total weights sum to 1.0
	total := weights.PriceWeight + weights.LatencyWeight + weights.FailureRateWeight
	if total < 0.99 || total > 1.01 {
		t.Errorf("Weights should sum to 1.0, got %f", total)
	}

	t.Logf("Weights: Price=%f, Latency=%f, FailureRate=%f",
		weights.PriceWeight, weights.LatencyWeight, weights.FailureRateWeight)
}

// Helper functions
func intPtr(i int) *int {
	return &i
}

func float64Ptr(f float64) *float64 {
	return &f
}
