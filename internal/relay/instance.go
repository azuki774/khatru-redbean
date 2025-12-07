package relay

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fiatjaf/khatru"
	"github.com/fiatjaf/khatru/policies"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
	"go.uber.org/zap"
)

type instance struct {
	Port string
	Info *nip11.RelayInformationDocument
}

func NewInstance(port string, info *nip11.RelayInformationDocument) *instance {
	return &instance{
		Port: port,
		Info: info,
	}
}

func (i *instance) Start(ctx context.Context) {
	// create the relay instance
	relay := khatru.NewRelay()

	// set up some basic properties (will be returned on the NIP-11 endpoint)
	relay.Info = i.Info

	// you must bring your own storage scheme -- if you want to have any
	store := make(map[string]*nostr.Event, 120)

	// set up the basic relay functions
	relay.StoreEvent = append(relay.StoreEvent,
		func(ctx context.Context, event *nostr.Event) error {
			store[event.ID] = event
			return nil
		},
	)
	relay.QueryEvents = append(relay.QueryEvents,
		func(ctx context.Context, filter nostr.Filter) (chan *nostr.Event, error) {
			ch := make(chan *nostr.Event)
			go func() {
				for _, evt := range store {
					if filter.Matches(evt) {
						ch <- evt
					}
				}
				close(ch)
			}()
			return ch, nil
		},
	)
	relay.DeleteEvent = append(relay.DeleteEvent,
		func(ctx context.Context, event *nostr.Event) error {
			delete(store, event.ID)
			return nil
		},
	)

	// there are many other configurable things you can set
	relay.RejectEvent = append(relay.RejectEvent,
		// built-in policies
		policies.ValidateKind,

		// define your own policies
		policies.PreventLargeTags(100),
		func(ctx context.Context, event *nostr.Event) (reject bool, msg string) {
			return false, "" // anyone else can
		},
	)

	// you can request auth by rejecting an event or a request with the prefix "auth-required: "
	relay.RejectFilter = append(relay.RejectFilter,
		// built-in policies
		policies.NoComplexFilters,

		// define your own policies
		func(ctx context.Context, filter nostr.Filter) (reject bool, msg string) {
			// TODO: 一旦オールOKにする
			pubkey := khatru.GetAuthed(ctx)
			zap.S().Infow("receive request", "pubkey", pubkey)
			return false, ""
		},
	)
	// check the docs for more goodies!

	mux := relay.Router()
	// set up other http handlers
	mux.HandleFunc("/", relay.HandleNIP11)
	mux.HandleFunc("/.well-known/nostr.json", relay.HandleNIP11)

	// start the server with graceful shutdown
	server := &http.Server{
		Addr:    fmt.Sprintf(":%s", i.Port),
		Handler: relay,
	}

	// シグナルハンドリング用のcontextを作成
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// サーバーをgoroutineで起動
	serverErr := make(chan error, 1)
	go func() {
		zap.S().Infow("waiting for requests", "port", i.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// シグナルまたはエラーを待機
	select {
	case <-sigCtx.Done():
		zap.S().Info("shutting down gracefully...")
		// graceful shutdown用のcontextを作成（30秒のタイムアウト）
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// graceful shutdownを実行
		if err := server.Shutdown(shutdownCtx); err != nil {
			zap.S().Errorw("server forced to shutdown", "error", err)
		} else {
			zap.S().Info("server exited gracefully")
		}
	case err := <-serverErr:
		zap.S().Errorw("server error", "error", err)
	}
}
