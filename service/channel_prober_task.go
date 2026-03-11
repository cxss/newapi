package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"

	"github.com/bytedance/gopkg/util/gopool"
)

const (
	proberTickInterval   = 3 * time.Minute
	proberMaxConcurrency = 8
	proberTimeout        = 15 * time.Second
)

var (
	channelProberOnce    sync.Once
	channelProberRunning atomic.Bool
)

// StartChannelProberTask launches the background prober. Safe to call multiple times.
func StartChannelProberTask() {
	channelProberOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			ctx := context.Background()
			logger.LogInfo(ctx, fmt.Sprintf("channel prober started: interval=%s concurrency=%d",
				proberTickInterval, proberMaxConcurrency))
			ticker := time.NewTicker(proberTickInterval)
			defer ticker.Stop()
			runProberOnce(ctx)
			for range ticker.C {
				runProberOnce(ctx)
			}
		})
	})
}

func runProberOnce(ctx context.Context) {
	if !channelProberRunning.CompareAndSwap(false, true) {
		return
	}
	defer channelProberRunning.Store(false)

	channels, err := model.GetEnabledChannelsForProbe()
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("prober: failed to load channels: %v", err))
		return
	}

	sem := make(chan struct{}, proberMaxConcurrency)
	var wg sync.WaitGroup

	for _, ch := range channels {
		ch := ch
		wg.Add(1)
		sem <- struct{}{}
		gopool.Go(func() {
			defer wg.Done()
			defer func() { <-sem }()
			probeChannel(ctx, ch)
		})
	}
	wg.Wait()
}

func probeChannel(ctx context.Context, channel *model.Channel) {
	// Skip channels still in cooldown (unless in probe window)
	if channel.IsTempDisabled() && !channel.IsProbeCandidate() {
		return
	}

	testModel := resolveProbeModel(channel)

	start := time.Now()
	success, statusCode := pingChannelHTTP(channel, testModel)
	latencyMs := int(time.Since(start).Milliseconds())

	if err := channel.UpdateMetrics(latencyMs, success); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("prober: update metrics failed channel=%d: %v", channel.Id, err))
	}

	if success {
		logger.LogInfo(ctx, fmt.Sprintf(
			"prober: channel=%d name=%q model=%s latency=%dms ok",
			channel.Id, channel.Name, testModel, latencyMs,
		))
	} else {
		logger.LogWarn(ctx, fmt.Sprintf(
			"prober: channel=%d name=%q model=%s http=%d fail consecutive=%d",
			channel.Id, channel.Name, testModel, statusCode, channel.ConsecutiveFails,
		))
	}
}

func resolveProbeModel(channel *model.Channel) string {
	if channel.TestModel != nil && *channel.TestModel != "" {
		return *channel.TestModel
	}
	models := channel.GetModels()
	if len(models) > 0 {
		return models[0]
	}
	return "gpt-4o-mini"
}

// pingChannelHTTP sends a minimal chat completion request directly via HTTP.
// Returns (success, httpStatusCode).
func pingChannelHTTP(channel *model.Channel, modelName string) (bool, int) {
	baseURL := channel.GetBaseURL()
	if baseURL == "" {
		return false, 0
	}

	body := fmt.Sprintf(
		`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":1}`,
		modelName,
	)

	req, err := http.NewRequest("POST", baseURL+"/v1/chat/completions", bytes.NewBufferString(body))
	if err != nil {
		return false, 0
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+channel.Key)

	// Use a short-timeout client for probes to avoid blocking the prober goroutine
	client := &http.Client{Timeout: proberTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return false, 0
	}
	defer resp.Body.Close()

	return resp.StatusCode >= 200 && resp.StatusCode < 300, resp.StatusCode
}
