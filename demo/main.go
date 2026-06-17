package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/Sashreek007/mint/demo/product"
	keysvc "github.com/Sashreek007/mint/keyservice-go"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	mintURL := getenv("MINT_URL", "http://localhost:8080")
	addr := getenv("LISTEN_ADDR", ":9000")

	// Fail-closed by default: if Mint is unreachable the middleware returns 503
	// rather than letting unauthenticated traffic through.
	client := keysvc.New(mintURL)

	slog.Info("demo stock api up", "addr", addr, "mint", mintURL)
	if err := http.ListenAndServe(addr, product.Handler(client)); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
