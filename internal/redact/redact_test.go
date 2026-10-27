package redact

import (
	"strings"
	"testing"
)

func TestText(t *testing.T) {
	fakeKey := "AKIA" + strings.Repeat("X", 16)
	for in, want := range map[string]string{
		"verbose":                            "verbose",
		"postgres://app:s3cret@db:5432/shop": "postgres://[redacted]@db:5432/shop",
		"--password=hunter2 --port=80":       "--password=[redacted] --port=80",
		"key " + fakeKey:                     "key [redacted key]",
	} {
		got, hid := Text(in)
		if got != want || hid != (in != want) {
			t.Errorf("Text(%q) = %q, %v; want %q", in, got, hid, want)
		}
	}
}

func TestSensitiveName(t *testing.T) {
	for name, want := range map[string]bool{
		"DB_PASSWORD": true, "API_KEY": true, "stripe.apiKey": true, "AUTH_TOKEN": true, "PRIVATE_KEY": true,
		"LOG_LEVEL": false, "PAYMENT_URL": false, "CACHE_TTL": false,
	} {
		if got := SensitiveName(name); got != want {
			t.Errorf("SensitiveName(%q) = %v", name, got)
		}
	}
}
