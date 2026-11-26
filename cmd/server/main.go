// Command server is the authenticated HTTP control plane for ExpenseDrift.
// It runs on SnapDeploy's free tier (linux/arm64, distroless, nonroot),
// binding to 0.0.0.0:$PORT (default 8080) and waking on browser visits.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"expensedrift/internal/collector"
	"expensedrift/internal/fetcher"
	"expensedrift/internal/extractor"
	"expensedrift/internal/storage"
	"expensedrift/internal/web"
)

var (
	addr            string
	port            string
	collectInterval time.Duration
)

func init() {
	flag.StringVar(&addr, "addr", "", "Listen address (overrides $PORT)")
	flag.StringVar(&port, "port", "", "TCP port (overrides $PORT, default 8080)")
	flag.DurationVar(&collectInterval, "collect-interval", 7*time.Minute+30*time.Second, "Interval between automatic collection runs (0 disables auto-collection)")
}

func main() {
	flag.Parse()

	cfg := storage.FromEnv()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Validate required configuration.
	if cfg.CFAccountID == "" || cfg.CFAPIToken == "" || cfg.CFD1DatabaseID == "" {
		log.Error("missing required configuration",
			"missing", []string{"CF_ACCOUNT_ID", "CF_API_TOKEN", "CF_D1_DATABASE_ID"})
		os.Exit(1)
	}

	// Resolve bind address per SnapDeploy contract: 0.0.0.0:$PORT (default 8080).
	listenAddr := resolveAddr(addr, port)

	d1 := storage.NewD1Client(cfg)
	kv := storage.NewKVClient(cfg)
	fc := fetcher.New()
	ext := extractor.New(nil)

	srv := web.NewServer(cfg, d1, kv, fc, ext)

	mux := http.NewServeMux()
	srv.Routes(mux)

	// Start background collection scheduler so SnapDeploy container stays alive
	// and collection triggers automatically every collectInterval (default 7m30s).
	if collectInterval > 0 {
		go startCollectionScheduler(log, d1, kv, fc, ext, collectInterval)
		log.Info("collection scheduler enabled", "interval", collectInterval.String())
	}

	log.Info("server starting", "listen_addr", listenAddr, "d1_database", cfg.CFD1DatabaseID, "s3_bucket", cfg.S3Bucket)

	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Error("server exited", "error", err)
		os.Exit(1)
	}
}

// startCollectionScheduler runs collector.Run on a fixed ticker in the background.
// A mutex prevents overlapping runs if a collection takes longer than the interval.
func startCollectionScheduler(log *slog.Logger, d1 *storage.D1Client, kv *storage.KVClient, fc *fetcher.Fetcher, ext *extractor.Extractor, interval time.Duration) {
	var mu sync.Mutex
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		if !mu.TryLock() {
			log.Warn("previous collection run still in progress, skipping scheduled run")
			continue
		}
		go func() {
			defer mu.Unlock()
			ctx := context.Background()
			if _, err := collector.Run(ctx, log, d1, kv, fc, ext, "", "", false, false); err != nil {
				log.Error("scheduled collection run failed", "error", err)
			}
		}()
	}
}

func resolveAddr(addr, port string) string {
	if addr != "" {
		return addr
	}
	if port != "" {
		return "0.0.0.0:" + port
	}
	p := getEnv("PORT", "8080")
	return "0.0.0.0:" + p
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
