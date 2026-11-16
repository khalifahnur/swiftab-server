package util

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

const expoPushURL = "https://exp.host/--/api/v2/push/send"

type PushMessage struct {
	Title string
	Body  string
	// Data is delivered to the app so it can deep-link (for example to an order).
	Data map[string]string
	// Channel is the Android notification channel; empty uses "default".
	Channel string
}

var pushClient = &http.Client{Timeout: 10 * time.Second}

// IsExpoPushToken reports whether a string has the shape Expo issues.
func IsExpoPushToken(token string) bool {
	return (strings.HasPrefix(token, "ExponentPushToken[") || strings.HasPrefix(token, "ExpoPushToken[")) &&
		strings.HasSuffix(token, "]") && len(token) < 200
}

// SendPush delivers a notification to Expo push tokens in the background.
// Delivery is best effort: failures are logged and never block a request.
func SendPush(tokens []string, msg PushMessage) {
	var valid []string
	seen := map[string]bool{}
	for _, t := range tokens {
		if IsExpoPushToken(t) && !seen[t] {
			seen[t] = true
			valid = append(valid, t)
		}
	}
	if len(valid) == 0 {
		return
	}

	channel := msg.Channel
	if channel == "" {
		channel = "default"
	}

	go func() {
		// Expo accepts at most 100 messages per request.
		for start := 0; start < len(valid); start += 100 {
			end := start + 100
			if end > len(valid) {
				end = len(valid)
			}
			batch := make([]map[string]interface{}, 0, end-start)
			for _, token := range valid[start:end] {
				batch = append(batch, map[string]interface{}{
					"to":        token,
					"title":     msg.Title,
					"body":      msg.Body,
					"data":      msg.Data,
					"sound":     "default",
					"priority":  "high",
					"channelId": channel,
				})
			}
			payload, err := json.Marshal(batch)
			if err != nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, expoPushURL, bytes.NewReader(payload))
			if err != nil {
				cancel()
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			resp, err := pushClient.Do(req)
			if err != nil {
				log.Printf("push delivery failed: %v", err)
				cancel()
				continue
			}
			resp.Body.Close()
			cancel()
			if resp.StatusCode >= 300 {
				log.Printf("push delivery returned status %d", resp.StatusCode)
			}
		}
	}()
}
