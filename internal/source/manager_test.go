package source

import "testing"

// PickDomain 是渠道 domain 的打底：优先期望值，不支持时回落第一个可用域。
func TestPickDomain(t *testing.T) {
	feedOnly := Info{ID: "feed", Domains: []Domain{DomainFeed}}
	liveOnly := Info{ID: "live", Domains: []Domain{DomainLive}}
	both := Info{ID: "both", Domains: []Domain{DomainLive, DomainFeed}}
	none := Info{ID: "none"}
	weird := Info{ID: "weird", Domains: []Domain{Domain("custom")}}

	cases := []struct {
		name string
		info Info
		want Domain
		got  Domain
		ok   bool
	}{
		{"期望值可用", both, DomainFeed, DomainFeed, true},
		{"期望值不可用则回落", liveOnly, DomainFeed, DomainLive, true},
		{"未指定取第一个可用域", both, "", DomainLive, true},
		{"未指定且仅有 feed", feedOnly, "", DomainFeed, true},
		{"没有可用域", none, DomainLive, "", false},
		{"只有未知域", weird, DomainFeed, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := PickDomain(tc.info, tc.want)
			if got != tc.got || ok != tc.ok {
				t.Fatalf("PickDomain(%q, %q) = (%q, %v)，期望 (%q, %v)", tc.info.ID, tc.want, got, ok, tc.got, tc.ok)
			}
		})
	}
}
