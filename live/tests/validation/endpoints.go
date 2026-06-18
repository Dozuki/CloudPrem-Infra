package validation

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func normalizeURL(u string) string {
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	return "https://" + u
}

// CheckEndpoint issues HTTPS GETs (TLS verification skipped — ephemeral test
// stacks use internal/self-signed certs; this is a liveness probe, no secrets)
// and returns nil once a 200 is seen, retrying up to attempts times.
func CheckEndpoint(rawURL string, attempts int, interval time.Duration) error {
	url := normalizeURL(rawURL)
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	var last error
	for i := 0; i < attempts; i++ {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("status %d from %s", resp.StatusCode, url)
		} else {
			last = err
		}
		time.Sleep(interval)
	}
	return fmt.Errorf("endpoint %s not healthy after %d attempts: %w", url, attempts, last)
}

// CheckEndpoints validates the dashboard and app URLs.
func CheckEndpoints(o StackOutputs) error {
	for _, u := range []string{o.DashboardURL, o.DozukiURL} {
		if u == "" {
			continue
		}
		if err := CheckEndpoint(u, 120, 30*time.Second); err != nil {
			return err
		}
	}
	return nil
}
