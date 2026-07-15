// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_serve

package cli

import (
	"context"
	"flag"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/ipn"
	"tailscale.com/tailcfg"
)

func init() {
	maybeFunnelCmd = funnelCmd
}

var funnelCmd = func() *ffcli.Command {
	se := &serveEnv{lc: &localClient}
	// previously used to serve legacy newFunnelCommand unless useWIPCode is true
	// change is limited to make a revert easier and full cleanup to come after the release.
	// TODO(tylersmalley): cleanup and removal of newFunnelCommand as of 2023-10-16
	return newServeV2Command(se, funnel)
}

// newFunnelCommand returns a new "funnel" subcommand using e as its environment.
// The funnel subcommand is used to turn on/off the Funnel service.
// Funnel is off by default.
// Funnel allows you to publish a 'tailscale serve' server publicly, open to the
// entire internet.
// newFunnelCommand shares the same serveEnv as the "serve" subcommand. See
// newServeCommand and serve.go for more details.
func newFunnelCommand(e *serveEnv) *ffcli.Command {
	return &ffcli.Command{
		Name:      "funnel",
		ShortHelp: "开启/关闭 Funnel 服务",
		ShortUsage: strings.Join([]string{
			"tailscale funnel <serve-port> {on|off}",
			"tailscale funnel status [--json]",
		}, "\n"),
		LongHelp: strings.Join([]string{
			"Funnel 允许你将 'tailscale serve'",
			"服务器公开发布到整个互联网。",
			"",
			"关闭 Funnel 仅会停止对互联网的发布服务，",
			"不会影响对你自己的 tailnet 的服务。",
		}, "\n"),
		Exec: e.runFunnel,
		Subcommands: []*ffcli.Command{
			{
				Name:       "status",
				Exec:       e.runServeStatus,
				ShortUsage: "tailscale funnel status [--json]",
				ShortHelp:  "显示当前 serve/funnel 状态",
				FlagSet: e.newFlags("funnel-status", func(fs *flag.FlagSet) {
					fs.BoolVar(&e.json, "json", false, "输出 JSON")
				}),
			},
		},
	}
}

// runFunnel is the entry point for the "tailscale funnel" subcommand and
// manages turning on/off funnel. Funnel is off by default.
//
// Note: funnel is only supported on single DNS name for now. (2022-11-15)
func (e *serveEnv) runFunnel(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return flag.ErrHelp
	}

	var on bool
	switch args[1] {
	case "on", "off":
		on = args[1] == "on"
	default:
		return flag.ErrHelp
	}
	sc, err := e.lc.GetServeConfig(ctx)
	if err != nil {
		return err
	}
	if sc == nil {
		sc = new(ipn.ServeConfig)
	}

	port64, err := strconv.ParseUint(args[0], 10, 16)
	if err != nil {
		return err
	}
	port := uint16(port64)

	if on {
		// Don't block from turning off existing Funnel if
		// network configuration/capabilities have changed.
		// Only block from starting new Funnels.
		if err := e.verifyFunnelEnabled(ctx, port); err != nil {
			return err
		}
	}

	st, err := e.getLocalClientStatusWithoutPeers(ctx)
	if err != nil {
		return fmt.Errorf("获取客户端状态出错：%w", err)
	}
	dnsName := strings.TrimSuffix(st.Self.DNSName, ".")
	hp := ipn.HostPort(dnsName + ":" + strconv.Itoa(int(port)))
	if on == sc.AllowFunnel[hp] {
		printFunnelWarning(sc)
		// Nothing to do.
		return nil
	}
	sc.SetFunnel(dnsName, port, on)

	if err := e.lc.SetServeConfig(ctx, sc); err != nil {
		return err
	}
	printFunnelWarning(sc)
	return nil
}

// verifyFunnelEnabled verifies that the self node is allowed to use Funnel.
//
// If Funnel is not yet enabled by the current node capabilities,
// the user is sent through an interactive flow to enable the feature.
// Once enabled, verifyFunnelEnabled checks that the given port is allowed
// with Funnel.
//
// If an error is reported, the CLI should stop execution and return the error.
//
// verifyFunnelEnabled may refresh the local state and modify the st input.
func (e *serveEnv) verifyFunnelEnabled(ctx context.Context, port uint16) error {
	enableErr := e.enableFeatureInteractive(ctx, "funnel", tailcfg.CapabilityHTTPS, tailcfg.NodeAttrFunnel)
	st, statusErr := e.getLocalClientStatusWithoutPeers(ctx) // get updated status; interactive flow may block
	switch {
	case statusErr != nil:
		return fmt.Errorf("获取客户端状态出错：%w", statusErr)
	case enableErr != nil:
		// enableFeatureInteractive is a new flow behind a control server
		// feature flag. If anything caused it to error, fallback to using
		// the old CheckFunnelAccess call. Likely this domain does not have
		// the feature flag on.
		// TODO(sonia,tailscale/corp#10577): Remove this fallback once the
		// control flag is turned on for all domains.
		if err := ipn.CheckFunnelAccess(port, st.Self); err != nil {
			return err
		}
	default:
		// Done with enablement, make sure the requested port is allowed.
		if err := ipn.CheckFunnelPort(port, st.Self); err != nil {
			return err
		}
	}
	return nil
}

// printFunnelWarning prints a warning if the Funnel is on but there is no serve
// config for its host:port.
func printFunnelWarning(sc *ipn.ServeConfig) {
	var warn bool
	for hp, a := range sc.AllowFunnel {
		if !a {
			continue
		}
		_, portStr, _ := net.SplitHostPort(string(hp))
		p, _ := strconv.ParseUint(portStr, 10, 16)
		if _, ok := sc.TCP[uint16(p)]; !ok {
			warn = true
			fmt.Fprintf(Stderr, "\n警告：%s 的 funnel 已开启，但没有 serve 配置\n", hp)
		}
	}
	if warn {
		fmt.Fprintf(Stderr, "         运行：`tailscale serve --help` 查看如何配置处理器\n")
	}
}

func init() {
	hookPrintFunnelStatus.Set(printFunnelStatus)
}

// printFunnelStatus prints the status of the funnel, if it's running.
// It prints nothing if the funnel is not running.
func printFunnelStatus(ctx context.Context) {
	sc, err := localClient.GetServeConfig(ctx)
	if err != nil {
		outln()
		printf("# Funnel：\n")
		printf("#     - 无法获取 Funnel 状态：%v\n", err)
		return
	}
	if !sc.IsFunnelOn() {
		return
	}
	outln()
	printf("# Funnel 已开启：\n")
	for hp, on := range sc.AllowFunnel {
		if !on { // if present, should be on
			continue
		}
		sni, portStr, _ := net.SplitHostPort(string(hp))
		p, _ := strconv.ParseUint(portStr, 10, 16)
		isTCP := sc.IsTCPForwardingOnPort(uint16(p), noService)
		url := "https://"
		if isTCP {
			url = "tcp://"
		}
		url += sni
		if isTCP || p != 443 {
			url += ":" + portStr
		}
		printf("#     - %s\n", url)
	}
	outln()
}
