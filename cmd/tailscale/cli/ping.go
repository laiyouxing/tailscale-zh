// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/client/local"
	"tailscale.com/cmd/tailscale/cli/ffcomplete"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

var pingCmd = &ffcli.Command{
	Name:       "ping",
	ShortUsage: "tailscale ping <hostname-or-IP>",
	ShortHelp:  "在 Tailscale 层对主机执行 ping，查看其路由路径",
	LongHelp: strings.TrimSpace(`

'tailscale ping' 命令从 Tailscale 层对对等节点执行 ping，
并报告每次响应所经过的路由。前几次 ping 很可能会经过 DERP
（Tailscale 的 TCP 中继协议），直到 NAT 穿透找到一条直连路径。

如果 'tailscale ping' 能通，但普通 ping 不通，说明某一方的
操作系统防火墙在拦截数据包；'tailscale ping' 不会将数据包
注入到任何一方的 TUN 设备中。

默认情况下，'tailscale ping' 在发送 10 次 ping 或建立起一条
直连（非 DERP）路径后停止，以先到者为准。

提供的 hostname 必须能解析为或本身就是一个 Tailscale IP
（如 100.x.y.z），或者是某个 Tailscale 中继节点通告的子网 IP。

`),
	Exec: runPing,
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("ping")
		fs.BoolVar(&pingArgs.verbose, "verbose", false, "详细输出")
		fs.BoolVar(&pingArgs.untilDirect, "until-direct", true, "一旦建立直连路径即停止")
		fs.BoolVar(&pingArgs.tsmp, "tsmp", false, "执行 TSMP 层 ping（通过 WireGuard，但不经过任一主机的操作系统协议栈）")
		fs.BoolVar(&pingArgs.icmp, "icmp", false, "执行 ICMP 层 ping（通过 WireGuard，但不经过本地主机操作系统协议栈）")
		fs.BoolVar(&pingArgs.peerAPI, "peerapi", false, "尝试访问对等节点的 peerapi HTTP 服务器")
		fs.IntVar(&pingArgs.num, "c", 10, "要发送的最大 ping 次数。0 表示无限。")
		fs.DurationVar(&pingArgs.timeout, "timeout", 5*time.Second, "放弃一次 ping 前的超时时间")
		fs.IntVar(&pingArgs.size, "size", 0, "ping 报文的大小（仅 disco ping）。0 表示最小大小。")
		return fs
	})(),
}

func init() {
	ffcomplete.Args(pingCmd, func(args []string) ([]string, ffcomplete.ShellCompDirective, error) {
		if len(args) > 1 {
			return nil, ffcomplete.ShellCompDirectiveNoFileComp, nil
		}
		return completeHostOrIP(ffcomplete.LastArg(args))
	})
}

var pingArgs struct {
	num         int
	size        int
	untilDirect bool
	verbose     bool
	tsmp        bool
	icmp        bool
	peerAPI     bool
	timeout     time.Duration
}

func pingType() tailcfg.PingType {
	if pingArgs.tsmp {
		return tailcfg.PingTSMP
	}
	if pingArgs.icmp {
		return tailcfg.PingICMP
	}
	if pingArgs.peerAPI {
		return tailcfg.PingPeerAPI
	}
	return tailcfg.PingDisco
}

func runPing(ctx context.Context, args []string) error {
	st, err := localClient.Status(ctx)
	if err != nil {
		return fixTailscaledConnectError(err)
	}
	description, ok := isRunningOrStarting(st)
	if !ok {
		printf("%s\n", description)
		os.Exit(1)
	}

	if len(args) != 1 || args[0] == "" {
		return errors.New("用法：tailscale ping <主机名或 IP>")
	}
	var ip string

	hostOrIP := args[0]
	ip, self, err := tailscaleIPFromArg(ctx, hostOrIP)
	if err != nil {
		return err
	}
	if self {
		printf("%v 是本地 Tailscale IP\n", ip)
		return nil
	}

	if pingArgs.verbose && ip != hostOrIP {
		log.Printf("查找 %q => %q", hostOrIP, ip)
	}

	n := 0
	anyPong := false
	for {
		n++
		ctx, cancel := context.WithTimeout(ctx, pingArgs.timeout)
		pr, err := localClient.PingWithOpts(ctx, netip.MustParseAddr(ip), pingType(), local.PingOpts{Size: pingArgs.size})
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
			printf("对 %q 的 ping 超时\n", ip)
			if n == pingArgs.num {
				if !anyPong {
					return errors.New("无响应")
				}
				return nil
			}
				continue
			}
			return err
		}
		if pr.Err != "" {
			if pr.IsLocalIP {
				outln(pr.Err)
				return nil
			}
			return errors.New(pr.Err)
		}
		latency := time.Duration(pr.LatencySeconds * float64(time.Second)).Round(time.Millisecond)
		via := pr.Endpoint
		if pr.PeerRelay != "" {
			via = fmt.Sprintf("peer-relay(%s)", pr.PeerRelay)
		} else if pr.DERPRegionID != 0 {
			via = fmt.Sprintf("DERP(%s)", pr.DERPRegionCode)
		}
		if via == "" {
			// TODO(bradfitz): populate the rest of ipnstate.PingResult for TSMP queries?
			// For now just say which protocol it used.
			via = string(pingType())
		}
		if pingArgs.peerAPI {
			printf("命中 %s (%s) 的 peerapi，地址 %s，耗时 %s\n", pr.NodeIP, pr.NodeName, pr.PeerAPIURL, latency)
			return nil
		}
		anyPong = true
		extra := ""
		if pr.PeerAPIPort != 0 {
			extra = fmt.Sprintf(", %d", pr.PeerAPIPort)
		}
		printf("来自 %s (%s%s) 的 pong，经由 %v，耗时 %v\n", pr.NodeName, pr.NodeIP, extra, via, latency)
		if pingArgs.tsmp || pingArgs.icmp {
			return nil
		}
		if pr.Endpoint != "" && pingArgs.untilDirect {
			return nil
		}
		time.Sleep(time.Second)

		if n == pingArgs.num {
			if !anyPong {
				return errors.New("无响应")
			}
			if pingArgs.untilDirect {
				return errors.New("未建立直连连接")
			}
			return nil
		}
	}
}

func tailscaleIPFromArg(ctx context.Context, hostOrIP string) (ip string, self bool, err error) {
	// If the argument is an IP address, use it directly without any resolution.
	if net.ParseIP(hostOrIP) != nil {
		return hostOrIP, false, nil
	}

	// Otherwise, try to resolve it first from the network peer list.
	st, err := localClient.Status(ctx)
	if err != nil {
		return "", false, err
	}
	match := func(ps *ipnstate.PeerStatus) bool {
		return strings.EqualFold(hostOrIP, dnsOrQuoteHostname(st, ps)) || hostOrIP == ps.DNSName
	}
	for _, ps := range st.Peer {
		if match(ps) {
			if len(ps.TailscaleIPs) == 0 {
				return "", false, errors.New("找到了节点但没有 IP")
			}
			return ps.TailscaleIPs[0].String(), false, nil
		}
	}
	if match(st.Self) && len(st.Self.TailscaleIPs) > 0 {
		return st.Self.TailscaleIPs[0].String(), true, nil
	}

	// Finally, use DNS.
	var res net.Resolver
	if addrs, err := res.LookupHost(ctx, hostOrIP); err != nil {
		return "", false, fmt.Errorf("查找 %q 的 IP 出错：%v", hostOrIP, err)
	} else if len(addrs) == 0 {
		return "", false, fmt.Errorf("未找到 %q 的 IP", hostOrIP)
	} else {
		return addrs[0], false, nil
	}
}
