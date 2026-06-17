//go:build !live

package integration

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/compose"
	"github.com/testcontainers/testcontainers-go/wait"
)

// mintURL is the nginx base URL of the stack brought up by TestMain.
var mintURL string

// TestMain (default mode) brings up docker-compose.test.yml once for the whole
// test binary, points the suite at the mapped nginx port, runs the tests, then
// tears the stack down. `-tags live` excludes this file and uses the running
// dev stack instead (see setup_live_test.go).
func TestMain(m *testing.M) {
	ctx := context.Background()

	stack, err := compose.NewDockerComposeWith(
		compose.StackIdentifier("mint-integration"),
		compose.WithStackFiles("../docker-compose.test.yml"),
	)
	if err != nil {
		log.Fatalf("compose init: %v", err)
	}

	upErr := stack.
		WaitForService("nginx", wait.ForHTTP("/healthz").
			WithPort("80/tcp").
			WithStartupTimeout(3*time.Minute)).
		Up(ctx, compose.Wait(true))
	if upErr != nil {
		_ = stack.Down(ctx, compose.RemoveOrphans(true))
		log.Fatalf("bring up test stack: %v", upErr)
	}

	if c, err := stack.ServiceContainer(ctx, "nginx"); err == nil {
		host, _ := c.Host(ctx)
		if port, perr := c.MappedPort(ctx, "80/tcp"); perr == nil {
			mintURL = fmt.Sprintf("http://%s:%s", host, port.Port())
		}
	}

	code := 1
	if mintURL != "" {
		code = m.Run()
	} else {
		log.Println("could not resolve nginx mapped port")
	}

	_ = stack.Down(ctx, compose.RemoveOrphans(true), compose.RemoveImagesLocal)
	os.Exit(code)
}

func mintEnv(t *testing.T) string {
	t.Helper()
	if mintURL == "" {
		t.Fatal("test stack not initialized")
	}
	return mintURL
}
