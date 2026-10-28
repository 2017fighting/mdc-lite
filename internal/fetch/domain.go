package fetch

import (
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// bucketKey is the rate-limit and cooldown key of a URL.
//
// It is the registrable domain (eTLD+1), not the host: `pics.dmm.co.jp` and
// `www.dmm.co.jp` are two hosts of one site, so one bucket and one cooldown cover
// both. Sharing one bucket per registrable domain is what keeps the app from
// hitting one operator from two subdomains at twice the rate.
//
// An address with no registrable domain — the IP literal the acceptance fixtures
// use, `localhost`, a bare hostname — falls back to host:port. There is no site to
// be polite to there, and merging two local servers into one bucket would make a
// test suite's rate limits leak between its cases.
func bucketKey(u *url.URL) string {
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return strings.ToLower(u.Host)
	}
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		// An IP literal or a single-label host (`localhost`, a compose service name)
		// is not a site; the port is what keeps two of them apart.
		return strings.ToLower(u.Host)
	}
	return registrableDomain(host)
}

// registrableDomain returns the eTLD+1 of a DNS name, from the public suffix list
// the binary embeds (golang.org/x/net/publicsuffix): `co.jp` is a suffix, so
// `pics.dmm.co.jp` and `www.dmm.co.jp` both answer `dmm.co.jp`, and `a.github.io`
// and `b.github.io` both answer `github.io` — the last two labels would have
// merged the first pair only because someone had added `co.jp` by hand.
//
// A host the list cannot resolve (an unknown TLD in a test, a name with no eTLD)
// degrades to its last two labels: over-bucketing is a slow library, under-bucketing
// is a ban.
func registrableDomain(host string) string {
	if domain, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return domain
	}
	labels := strings.Split(host, ".")
	if len(labels) <= 2 {
		return host
	}
	return labels[len(labels)-2] + "." + labels[len(labels)-1]
}
