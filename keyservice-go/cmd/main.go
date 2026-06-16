package main

import (
	"context"
	"fmt"
	"os"
	"time"

	keysvc "github.com/Sashreek007/mint/keyservice-go"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: go run ./cmd/check <api-key>")
		os.Exit(1)
	}
	c := keysvc.New("http://localhost:8080")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := c.Validate(ctx, os.Args[1])
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	name := map[keysvc.Outcome]string{
		keysvc.Allowed: "Allowed", keysvc.Invalid: "Invalid",
		keysvc.RateLimited: "RateLimited", keysvc.QuotaExceeded: "QuotaExceeded",
	}[res.Outcome]
	fmt.Printf("outcome=%s tenant=%s key=%s\n", name, res.TenantID, res.KeyID)
}
