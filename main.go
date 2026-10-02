package main

import (
	"os"

	"github.com/azuki774/khatru-redbean/cmd"
	logger "github.com/azuki774/khatru-redbean/internal/logger"
	"go.uber.org/zap"
)

func main() {
	os.Exit(run())
}

func run() int {
	glogger := logger.Load()
	defer glogger.Sync() // 必要

	if err := cmd.Execute(); err != nil {
		zap.S().Errorw("command failed", "error", err)
		return 1
	}
	return 0
}
