package cmd

import (
	"os/signal"
	"syscall"
	"time"

	"github.com/azuki774/khatru-redbean/internal/monitor"
	"github.com/spf13/cobra"
)

var monitorConfig = monitor.Config{Interval: time.Hour, MaxAge: 12 * time.Hour, Timeout: 30 * time.Second, Retries: 2, RetryInterval: 30 * time.Second, RemindInterval: 12 * time.Hour}
var monitorCmd = &cobra.Command{Use: "monitor", Short: "monitor a Nostr relay", Args: cobra.NoArgs,
	PreRunE: func(cmd *cobra.Command, args []string) error { return monitorConfig.Validate() },
	Run: func(cmd *cobra.Command, args []string) {
		ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		m := monitor.Monitor{Config: monitorConfig, Alert: monitor.LogAlert}
		m.Run(ctx)
	},
}

func init() {
	rootCmd.AddCommand(monitorCmd)
	f := monitorCmd.Flags()
	f.StringVar(&monitorConfig.Relay, "relay", "", "relay websocket URL")
	f.DurationVar(&monitorConfig.Interval, "interval", time.Hour, "check interval")
	f.DurationVar(&monitorConfig.MaxAge, "max-age", 12*time.Hour, "maximum acceptable event age")
	f.DurationVar(&monitorConfig.Timeout, "timeout", 30*time.Second, "per-attempt timeout")
	f.IntVar(&monitorConfig.Retries, "retries", 2, "additional attempts")
	f.DurationVar(&monitorConfig.RetryInterval, "retry-interval", 30*time.Second, "delay between attempts")
	f.DurationVar(&monitorConfig.RemindInterval, "remind-interval", 12*time.Hour, "abnormal reminder interval")
}
