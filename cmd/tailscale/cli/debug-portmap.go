// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ios && !ts_omit_debugportmapper

package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"time"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/client/local"
)

func init() {
	debugPortmapCmd = mkDebugPortmapCmd
}

func mkDebugPortmapCmd() *ffcli.Command {
	return &ffcli.Command{
		Name:       "portmap",
		ShortUsage: "tailscale debug portmap",
		Exec:       debugPortmap,
		ShortHelp:  "运行端口映射调试",
		FlagSet: (func() *flag.FlagSet {
			fs := newFlagSet("portmap")
			fs.DurationVar(&debugPortmapArgs.duration, "duration", 5*time.Second, "端口映射超时时间")
			fs.StringVar(&debugPortmapArgs.ty, "type", "", `端口映射调试类型（可选 ""、"pmp"、"pcp" 或 "upnp"）`)
			fs.StringVar(&debugPortmapArgs.gatewayAddr, "gateway-addr", "", `覆盖网关 IP（必须同时传入 --self-addr）`)
			fs.StringVar(&debugPortmapArgs.selfAddr, "self-addr", "", `覆盖本机 IP（必须同时传入 --gateway-addr）`)
			fs.BoolVar(&debugPortmapArgs.logHTTP, "log-http", false, `将所有 HTTP 请求和响应打印到日志`)
			return fs
		})(),
	}
}

var debugPortmapArgs struct {
	duration    time.Duration
	gatewayAddr string
	selfAddr    string
	ty          string
	logHTTP     bool
}

func debugPortmap(ctx context.Context, args []string) error {
	opts := &local.DebugPortmapOpts{
		Duration: debugPortmapArgs.duration,
		Type:     debugPortmapArgs.ty,
		LogHTTP:  debugPortmapArgs.logHTTP,
	}
	if (debugPortmapArgs.gatewayAddr != "") != (debugPortmapArgs.selfAddr != "") {
		return fmt.Errorf("若提供了 --gateway-addr 或 --self-addr 中的任意一个，则另一个也必须提供")
	}
	if debugPortmapArgs.gatewayAddr != "" {
		var err error
		opts.GatewayAddr, err = netip.ParseAddr(debugPortmapArgs.gatewayAddr)
		if err != nil {
			return fmt.Errorf("无效的 --gateway-addr：%w", err)
		}
		opts.SelfAddr, err = netip.ParseAddr(debugPortmapArgs.selfAddr)
		if err != nil {
			return fmt.Errorf("无效的 --self-addr：%w", err)
		}
	}
	rc, err := localClient.DebugPortmap(ctx, opts)
	if err != nil {
		return err
	}
	defer rc.Close()

	_, err = io.Copy(os.Stdout, rc)
	return err
}
