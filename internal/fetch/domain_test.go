package fetch

import (
	"net/url"
	"testing"
)

func TestBucketKeyIsTheRegistrableDomain(t *testing.T) {
	// The ticket's own example: two hosts of dmm are one bucket, because they are
	// one site and the rate limit is politeness towards a site.
	cases := map[string]string{
		"https://pics.dmm.co.jp/x.jpg":              "dmm.co.jp",
		"https://www.dmm.co.jp/number":              "dmm.co.jp",
		"https://api.video.dmm.co.jp/g":             "dmm.co.jp",
		"https://www.javbus.com/SSIS-001":           "javbus.com",
		"https://adult.contents.fc2.com/article/1/": "fc2.com",
		"https://c0.jdbstatic.com/covers/a.jpg":     "jdbstatic.com",
		"https://example.com/":                      "example.com",
		// A single-label host has no registrable domain to compute.
		"http://localhost:8080/x": "localhost:8080",
		// A hosting site's subdomains are separate sites under the public suffix list.
		"https://a.github.io/x": "a.github.io",
		"https://b.github.io/x": "b.github.io",
		// The acceptance fixtures serve from an IP literal: there is no site to be
		// polite to, so the fallback key keeps two local servers apart.
		"http://127.0.0.1:9001/a": "127.0.0.1:9001",
		"http://[::1]:9002/a":     "[::1]:9002",
	}
	for raw, want := range cases {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got := bucketKey(u); got != want {
			t.Errorf("bucketKey(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestRegistrableDomainDegradesForAHostWithNoETLD(t *testing.T) {
	// A host the public suffix list cannot resolve must over-bucket (last two
	// labels), never under-bucket: two hosts of one unknown site sharing a bucket is
	// a slow library, one site split across two buckets is a ban.
	if got := registrableDomain("a.b.invalid"); got != "b.invalid" {
		t.Errorf("registrableDomain = %q, want the last two labels", got)
	}
}
