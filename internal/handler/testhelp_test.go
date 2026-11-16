package handler

import (
	"time"

	"swiftab/server/internal/config"
)

func baseWithLocation(loc *time.Location) *Base {
	return &Base{Cfg: &config.Config{Location: loc}}
}
