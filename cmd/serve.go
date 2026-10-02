package cmd

import (
	"context"
	"os"
	"time"

	"github.com/azuki774/khatru-redbean/internal/config"
	"github.com/azuki774/khatru-redbean/internal/relay"
	"github.com/azuki774/khatru-redbean/internal/telemetry"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var servePort int

// serveCmd represents the serve command
var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "start server",
	Long:  `start server`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		provider, err := telemetry.NewProvider(ctx, config.Version)
		if err != nil {
			zap.S().Errorw("failed to initialize telemetry", "error", err)
		} else {
			provider.RegisterGlobal()
			defer func() {
				c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := provider.Shutdown(c); err != nil {
					zap.S().Errorw("failed to shutdown telemetry", "error", err)
				}
			}()
		}
		nip11 := config.NewNIP11InfoForredbean(
			os.Getenv("DESCRIPTION"),
			os.Getenv("PUBKEY"),
			os.Getenv("CONTACT"),
		)
		srv := relay.NewInstance(servePort, os.Getenv("DATABASE_URL"), os.Getenv("COUNTRY_ONLY"), nip11)

		zap.S().Infow("start server", "country_only", srv.CountryOnly)
		srv.Start(ctx)
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// serveCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// serveCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
	serveCmd.Flags().IntVarP(&servePort, "port", "p", 9999, "listen port")
}
