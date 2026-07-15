// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/ipn"
	"tailscale.com/types/logger"
	"tailscale.com/util/backoff"
)

var waitCmd = &ffcli.Command{
	Name:      "wait",
	ShortHelp: "等待 Tailscale 接口/IP 准备好以供绑定",
	LongHelp: strings.TrimSpace(`
等待 Tailscale 资源可用。截至 2026-01-02，唯一可等待的资源是
Tailscale 接口及其 IP 地址。

不带参数时，此命令会一直阻塞，直到 tailscaled 已启动、其后端正在运行，
且 Tailscale 接口已启动并分配了 Tailscale IP 地址。

若运行在用户态网络模式（userspace-networking）下，由于不存在物理网络接口，
此命令仅等待 tailscaled 与 Running 状态。

未来版本可能会支持等待其他类型的资源。

命令在成功时返回退出码 0，在失败或超时时返回非零退出码。

要等待特定类型的 IP 地址，可将 'tailscale ip' 与 'tailscale wait' 命令结合使用。
例如，等待一个 IPv4 地址：

    tailscale wait && tailscale ip --assert=<specific-IP-address>

Linux systemd 用户可以等待运行此命令的 "tailscale-online.target" 目标。

更一般地说，想要绑定（监听）Tailscale 接口或 IP 地址的服务可以这样运行它：
'tailscale wait && /path/to/service [...]'，以确保在程序启动前 Tailscale 已就绪。
`),

	ShortUsage: "tailscale wait",
	Exec:       runWait,
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("wait")
		fs.DurationVar(&waitArgs.timeout, "timeout", 0, "放弃前等待的时长（0 表示无限期等待）")
		return fs
	})(),
}

var waitArgs struct {
	timeout time.Duration
}

func runWait(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("意外的参数：%q", args)
	}
	if waitArgs.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, waitArgs.timeout)
		defer cancel()
	}

	bo := backoff.NewBackoff("wait", logger.Discard, 2*time.Second)
	for {
		_, err := localClient.StatusWithoutPeers(ctx)
		bo.BackOff(ctx, err)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	watcher, err := localClient.WatchIPNBus(ctx, ipn.NotifyInitialState)
	if err != nil {
		return err
	}
	defer watcher.Close()
	var firstIP netip.Addr
	for {
		not, err := watcher.Next()
		if err != nil {
			return err
		}
		if not.State != nil && *not.State == ipn.Running {

			st, err := localClient.StatusWithoutPeers(ctx)
			if err != nil {
				return err
			}
			if len(st.TailscaleIPs) > 0 {
				firstIP = st.TailscaleIPs[0]
				break
			}
		}
	}

	st, err := localClient.StatusWithoutPeers(ctx)
	if err != nil {
		return err
	}
	if !st.TUN {
		// No TUN; nothing more to wait for.
		return nil
	}

	// Verify we have an interface using that IP.
	for {
		err := checkForInterfaceIP(firstIP)
		if err == nil {
			return nil
		}
		bo.BackOff(ctx, err)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func checkForInterfaceIP(ip netip.Addr) error {
	ifs, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, ifi := range ifs {
		addrs, err := ifi.Addrs()
		if err != nil {
			return err
		}
		for _, addr := range addrs {
			var aip netip.Addr
			switch v := addr.(type) {
			case *net.IPNet:
				aip, _ = netip.AddrFromSlice(v.IP)
			case *net.IPAddr:
				aip, _ = netip.AddrFromSlice(v.IP)
			}
			if aip.Unmap() == ip {
				return nil
			}
		}
	}
	return fmt.Errorf("没有任何接口拥有 IP %v", ip)
}
