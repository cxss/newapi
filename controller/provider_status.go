package controller

import (
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

type ProviderStatusItem struct {
	Id           int     `json:"id"`
	Name         string  `json:"name"`
	Type         int     `json:"type"`
	Status       int     `json:"status"`
	StatusLabel  string  `json:"status_label"`
	Latency      int     `json:"latency_ms"`
	SuccessRate  float64 `json:"success_rate"`
	LastCheckAt  int64   `json:"last_check_at"`
	TempDisabled bool    `json:"temp_disabled"`
	CooldownSecs int64   `json:"cooldown_remaining_secs"`
}

// GetProviderStatus returns real-time health metrics for all channels.
// GET /api/status/providers
func GetProviderStatus(c *gin.Context) {
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}

	now := time.Now().Unix()
	items := make([]ProviderStatusItem, 0, len(channels))

	for _, ch := range channels {
		label := channelStatusLabel(ch.Status)
		tempDisabled := ch.IsTempDisabled()
		var cooldown int64
		if tempDisabled {
			cooldown = ch.TempDisabledUntil - now
			if cooldown < 0 {
				cooldown = 0
			}
			label = "temp_disabled"
		}

		items = append(items, ProviderStatusItem{
			Id:           ch.Id,
			Name:         ch.Name,
			Type:         ch.Type,
			Status:       ch.Status,
			StatusLabel:  label,
			Latency:      ch.GetLatency(),
			SuccessRate:  ch.GetSuccessRate(),
			LastCheckAt:  ch.LastCheckTime,
			TempDisabled: tempDisabled,
			CooldownSecs: cooldown,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    items,
	})
}

func channelStatusLabel(status int) string {
	switch status {
	case common.ChannelStatusEnabled:
		return "enabled"
	case common.ChannelStatusManuallyDisabled:
		return "disabled"
	case common.ChannelStatusAutoDisabled:
		return "auto_disabled"
	case common.ChannelStatusTempDisabled:
		return "temp_disabled"
	default:
		return "unknown"
	}
}
