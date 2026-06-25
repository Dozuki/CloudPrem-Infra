package main

import (
	"testing"
	"time"
)

func TestDeleteAfterFromTTL(t *testing.T) {
	got, err := time.Parse(time.RFC3339, deleteAfterFromTTL(24))
	if err != nil {
		t.Fatalf("not RFC3339: %v", err)
	}
	if d := time.Until(got); d < 23*time.Hour || d > 25*time.Hour {
		t.Fatalf("ttl ~24h expected, got %v", d)
	}
}
