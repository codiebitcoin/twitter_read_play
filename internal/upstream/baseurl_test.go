package upstream

import "testing"

// 上游没写 base_url 时必须回落到设置页的 socialdata_base_url，
// 否则「设置页改了地址、twitter 仍打官方 API」。
func TestProviderForFallsBackToGlobalBaseURL(t *testing.T) {
	SetDefaultBaseURLValue("http://twitter-read.default.svc.cluster.local")
	t.Cleanup(func() { SetDefaultBaseURLValue("") })

	// 需要 api 包提供的 model；这里只验证兜底值本身
	if got := defaultBaseURLValue(); got != "http://twitter-read.default.svc.cluster.local" {
		t.Fatalf("默认地址未生效: %q", got)
	}
	if got := orDefault("", defaultBaseURLValue()); got == "" {
		t.Fatal("空 base_url 应回落到全局默认")
	}
	if got := orDefault("http://x", defaultBaseURLValue()); got != "http://x" {
		t.Fatalf("上游自带地址应优先，实际 %q", got)
	}
}
