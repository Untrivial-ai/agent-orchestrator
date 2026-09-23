package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	endpoint := os.Getenv("AO_CLOUD_HEALTHCHECK_URL")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8080/readyz"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		exit(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		exit(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		exit(fmt.Errorf("readiness endpoint returned %s", response.Status))
	}
}

func exit(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
