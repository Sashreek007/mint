package main

import (
	"fmt"
	"net/http"

	keysvc "github.com/Sashreek007/mint/keyservice-go"
)

func main() {
	client := keysvc.New("http://localhost:8080")
	mux := http.NewServeMux()
	mux.HandleFunc("/forecast", func(w http.ResponseWriter, r *http.Request) {
		tenant, _ := keysvc.TenantID(r.Context()) // already verified by the middleware
		fmt.Fprintf(w, "forecast for tenant %s\n", tenant)
	})
	fmt.Println("demo product service on :9000")
	http.ListenAndServe(":9000", client.Middleware(mux)) // ← one line gates every route
}
