package machine

import (
	"net"
	"testing"
)

// 办公 VLAN 常见 /23：一刀切「只扫 /24」会漏掉一半设备，所以固定住预期范围：
// /22–/23 保留真实前缀，更宽的网段裁到接口所在的 /24。
func TestProbeRange(t *testing.T) {
	cases := []struct {
		cidr string
		want int
	}{
		{"192.168.32.181/23", 510}, // .32.1 – .33.254
		{"192.168.1.20/24", 254},
		{"10.0.0.5/16", 254},     // 裁到 10.0.0.0/24
		{"10.0.0.5/22", 1022},    // 保留 /22
		{"100.74.158.121/32", 0}, // 单主机网段不扫
	}
	for _, c := range cases {
		_, n, err := net.ParseCIDR(c.cidr)
		if err != nil {
			t.Fatalf("bad cidr %s: %v", c.cidr, err)
		}
		got := probeRange(hostOf(c.cidr), n)
		if len(got) != c.want {
			t.Errorf("%s: got %d hosts, want %d", c.cidr, len(got), c.want)
		}
		// 绝不产生网段外地址，也不包含网络号/广播地址。
		network := n.IP.To4()
		broadcast := make(net.IP, 4)
		for i := 0; i < 4; i++ {
			broadcast[i] = network[i] | ^n.Mask[i]
		}
		for _, a := range got {
			parsed := net.ParseIP(a)
			if parsed == nil || !n.Contains(parsed) {
				t.Errorf("%s: produced %q outside the subnet", c.cidr, a)
			}
			if parsed.Equal(network) || parsed.Equal(broadcast) {
				t.Errorf("%s: produced network/broadcast address %q", c.cidr, a)
			}
		}
	}
}

func TestIPLess(t *testing.T) {
	ips := []string{"10.0.0.10", "10.0.0.2", "10.0.0.1"}
	if !ipLess(ips[1], ips[0]) || !ipLess(ips[2], ips[1]) {
		t.Fatalf("IP order should be numeric, not lexicographic")
	}
}

func hostOf(cidr string) string {
	for i := len(cidr) - 1; i >= 0; i-- {
		if cidr[i] == '/' {
			return cidr[:i]
		}
	}
	return cidr
}
