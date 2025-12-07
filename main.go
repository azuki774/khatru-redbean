package main

import (
	"github.com/azuki774/khatru-redbean/cmd"
	logger "github.com/azuki774/khatru-redbean/internal/logger"
)

func main() {
	glogger := logger.Load()
	defer glogger.Sync() // 必要
	cmd.Execute()
}
