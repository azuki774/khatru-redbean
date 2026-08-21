package main

import (
	"context"
	"time"

	"github.com/azuki774/khatru-redbean/cmd"
	"github.com/azuki774/khatru-redbean/internal/config"
	logger "github.com/azuki774/khatru-redbean/internal/logger"
	"github.com/azuki774/khatru-redbean/internal/telemetry"
	"go.uber.org/zap"
)

func main() {
	glogger := logger.Load()
	defer glogger.Sync() // 必要

	provider, err := telemetry.NewProvider(context.Background(), config.Version)
	if err != nil {
		zap.S().Errorw("failed to initialize telemetry", "error", err)
	} else {
		provider.RegisterGlobal()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := provider.Shutdown(shutdownCtx); err != nil {
				zap.S().Errorw("failed to shutdown telemetry", "error", err)
			}
		}()
	}

	cmd.Execute()
}
