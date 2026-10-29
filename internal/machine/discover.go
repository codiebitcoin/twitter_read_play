// Package machine：局域网扫描（给「添加设备」表单用）。
//
// 设备在固定端口（DefaultPort）上提供 HTTP 服务，但固件并不广播 mDNS 服务记录，
// 办公网/家庭网又常是一个扁平的大子网 —— 因此直接按本机所在网段逐地址探测 TCP 端口，
// 比 mDNS 可靠得多。
package machine

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Discovered 一个在设备端口上接受 TCP 连接的地址。
type Discovered struct {
	IP   string `json:"ip"`
	Addr string `json:"addr"` // host:port，可直接粘到表单里
	RTT  int64  `json:"rtt_ms,omitempty"`
}

// DiscoverResult 是向导渲染所需的结果：本机网卡地址（让用户知道扫的是哪个网）
// + 实际扫描的网段 + 所有响应地址。
type DiscoverResult struct {
	LocalIPs []string     `json:"local_ips"`
	Subnets  []string     `json:"subnets"` // 实际扫描的 CIDR，如 "192.168.1.0/24"
	Devices  []Discovered `json:"devices"`
	Scanned  int          `json:"scanned"`
	Elapsed  int64        `json:"elapsed_ms"`
}

// Discover 扫描本机所连网段内每个主机的 `port` 端口。
//
// 范围规则：大于 /22 的网段会被裁到「接口地址所在的 /24」，否则一个 /16（更别说 /8）
// 意味着 6 万次拨号、UI 卡死；/22–/23 保留真实前缀（办公 VLAN 常见 /23，一刀切到 /24
// 会漏掉一半设备）。并发有上限，且整个扫描受 ctx 截止时间约束 —— 安静的 VLAN 上
// 每个地址都等满连接超时会拖很久。
func Discover(ctx context.Context, port string, perHost time.Duration) DiscoverResult {
	start := time.Now()
	out := DiscoverResult{LocalIPs: []string{}, Subnets: []string{}, Devices: []Discovered{}}

	self := map[string]bool{}
	candidates := map[string]bool{}
	for _, c := range interfaceV4() {
		self[c.ip] = true
		out.LocalIPs = append(out.LocalIPs, c.ip)
		for _, ip := range probeRange(c.ip, c.net) {
			candidates[ip] = true
		}
		out.Subnets = append(out.Subnets, c.cidr)
	}
	for ip := range self {
		delete(candidates, ip) // 永远不要把本机自己当成设备
	}
	out.Scanned = len(candidates)

	ips := make([]string, 0, len(candidates))
	for ip := range candidates {
		ips = append(ips, ip)
	}
	sort.Strings(ips)

	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		sem  = make(chan struct{}, 128)
		devs []Discovered
	)
	for _, ip := range ips {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(ip string) {
			defer wg.Done()
			defer func() { <-sem }()
			addr := net.JoinHostPort(ip, port)
			t0 := time.Now()
			conn, err := net.DialTimeout("tcp", addr, perHost)
			if err != nil {
				return
			}
			_ = conn.Close()
			mu.Lock()
			devs = append(devs, Discovered{IP: ip, Addr: addr, RTT: time.Since(t0).Milliseconds()})
			mu.Unlock()
		}(ip)
	}
	wg.Wait()

	sort.Slice(devs, func(i, j int) bool { return ipLess(devs[i].IP, devs[j].IP) })
	out.Devices = devs
	out.Elapsed = time.Since(start).Milliseconds()
	return out
}

type ifaceCIDR struct {
	ip   string
	net  *net.IPNet
	cidr string
}

func interfaceV4() []ifaceCIDR {
	var out []ifaceCIDR
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ifaceCIDR{ip: ip4.String(), net: ipnet, cidr: ipnet.String()})
		}
	}
	return out
}

// probeRange 返回某个接口地址需要尝试的主机地址。
func probeRange(ip string, n *net.IPNet) []string {
	if n == nil {
		return nil
	}
	ones, bits := n.Mask.Size()
	if bits != 32 {
		return nil
	}
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return nil
	}
	// /31、/32 没有可扫描的主机（点到点/单主机网段），直接跳过。
	if ones >= 31 {
		return nil
	}
	// 比 /22 更宽的网段裁到当前 /24：避免 /16 产生 6 万次拨号。
	if ones < 22 {
		parts[3] = "0"
		ones = 24
	}
	if ones >= 24 {
		base := fmt.Sprintf("%s.%s.%s", parts[0], parts[1], parts[2])
		out := make([]string, 0, 254)
		for i := 1; i <= 254; i++ {
			cand := base + "." + strconv.Itoa(i)
			if !n.Contains(net.ParseIP(cand)) {
				continue
			}
			out = append(out, cand)
		}
		return out
	}
	// /22–/23：按掩码覆盖的完整网络范围遍历（跳过网络号与广播地址）。
	network := n.IP.To4()
	if network == nil {
		return nil
	}
	start := uint32(network[0])<<24 | uint32(network[1])<<16 | uint32(network[2])<<8 | uint32(network[3])
	size := uint32(1) << (32 - ones)
	out := make([]string, 0, size-2)
	for off := uint32(1); off < size-1; off++ {
		v := start + off
		cand := fmt.Sprintf("%d.%d.%d.%d", byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
		if !n.Contains(net.ParseIP(cand)) {
			continue
		}
		out = append(out, cand)
	}
	return out
}

func ipLess(a, b string) bool {
	pa, pb := net.ParseIP(a).To4(), net.ParseIP(b).To4()
	if pa == nil || pb == nil {
		return a < b
	}
	for i := 0; i < 4; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}
