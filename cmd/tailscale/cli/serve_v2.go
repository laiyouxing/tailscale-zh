// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_serve

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/conffile"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/ipproto"
	"tailscale.com/util/dnsname"
	"tailscale.com/util/mak"
	"tailscale.com/util/prompt"
	"tailscale.com/util/set"
	"tailscale.com/util/slicesx"
	"tailscale.com/version"
)

type execFunc func(ctx context.Context, args []string) error

type commandInfo struct {
	Name      string
	ShortHelp string
	LongHelp  string
}

type serviceNameFlag struct {
	Value *tailcfg.ServiceName
}

func (s *serviceNameFlag) Set(sv string) error {
	if sv == "" {
		s.Value = new(tailcfg.ServiceName)
		return nil
	}
	v := tailcfg.ServiceName(sv)
	if err := v.Validate(); err != nil {
		return fmt.Errorf("无效的服务名称：%q", sv)
	}
	*s.Value = v
	return nil
}

// String returns the string representation of service name.
func (s *serviceNameFlag) String() string {
	return s.Value.String()
}

type bgBoolFlag struct {
	Value bool
	IsSet bool // tracks if the flag was set by the user
}

// Set sets the boolean flag and whether it's explicitly set by user based on the string value.
func (b *bgBoolFlag) Set(s string) error {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	b.Value = v
	b.IsSet = true
	return nil
}

// This is a hack to make the flag package recognize that this is a boolean flag.
func (b *bgBoolFlag) IsBoolFlag() bool { return true }

// String returns the string representation of the boolean flag.
func (b *bgBoolFlag) String() string {
	if !b.IsSet {
		return "default"
	}
	return strconv.FormatBool(b.Value)
}

type acceptAppCapsFlag struct {
	Value *[]tailcfg.PeerCapability
}

// An application capability name has the form {domain}/{name}.
// Both parts must use the (simplified) FQDN label character set.
// The "name" can contain forward slashes.
// \pL = Unicode Letter, \pN = Unicode Number, - = Hyphen
var validAppCap = regexp.MustCompile(`^([\pL\pN-]+\.)+[\pL\pN-]+\/[\pL\pN-/]+$`)

// Set appends s to the list of appCaps to accept.
func (u *acceptAppCapsFlag) Set(s string) error {
	if s == "" {
		return nil
	}
	appCaps := strings.SplitSeq(s, ",")
	for appCap := range appCaps {
		appCap = strings.TrimSpace(appCap)
		if !validAppCap.MatchString(appCap) {
			return fmt.Errorf("%q 不符合 {domain}/{name} 的形式，其中 domain 必须是一个完全限定域名", appCap)
		}
		*u.Value = append(*u.Value, tailcfg.PeerCapability(appCap))
	}
	return nil
}

// String returns the string representation of the slice of appCaps to accept.
func (u *acceptAppCapsFlag) String() string {
	s := make([]string, len(*u.Value))
	for i, v := range *u.Value {
		s[i] = string(v)
	}
	return strings.Join(s, ",")
}

var serveHelpCommon = strings.TrimSpace(`
<目标> 可以是一个文件、目录、文本，或者最常见的是本地机器上运行的
服务的位置。该位置服务可以表示为端口号（如 3000）、
部分 URL（如 localhost:3000），或包含路径的完整 URL（如 http://localhost:3000/foo）。
在类 Unix 系统上，你也可以指定一个 Unix 域套接字（如 unix:/tmp/myservice.sock）。

示例
  - 在前台暴露运行在 127.0.0.1:3000 的 HTTP 服务器：
    $ tailscale %[1]s 3000

  - 在后台暴露运行在 127.0.0.1:3000 的 HTTP 服务器：
    $ tailscale %[1]s --bg 3000

  - 暴露使用无效或自签名证书、位于 https://localhost:8443 的 HTTPS 服务器：
    $ tailscale %[1]s https+insecure://localhost:8443

  - 暴露监听在 Unix 套接字上的服务（仅限 Linux/macOS/BSD）：
    $ tailscale %[1]s unix:/var/run/myservice.sock

更多示例和使用场景请访问我们的文档站点 https://tailscale.com/kb/1247/funnel-serve-use-cases
`)

type serveMode int

const (
	serve serveMode = iota
	funnel
)

type serveType int

const (
	serveTypeHTTPS serveType = iota
	serveTypeHTTP
	serveTypeTCP
	serveTypeTLSTerminatedTCP
	serveTypeTUN
)

func serveTypeFromConfString(sp conffile.ServiceProtocol) (st serveType, ok bool) {
	switch sp {
	case conffile.ProtoHTTP:
		return serveTypeHTTP, true
	case conffile.ProtoHTTPS, conffile.ProtoHTTPSInsecure, conffile.ProtoFile:
		return serveTypeHTTPS, true
	case conffile.ProtoTCP:
		return serveTypeTCP, true
	case conffile.ProtoTLSTerminatedTCP:
		return serveTypeTLSTerminatedTCP, true
	case conffile.ProtoTUN:
		return serveTypeTUN, true
	}
	return -1, false
}

const noService tailcfg.ServiceName = ""

var infoMap = map[serveMode]commandInfo{
	serve: {
		Name:      "serve",
		ShortHelp: "在你的 tailnet 内提供服务内容与本地服务器",
		LongHelp: strings.Join([]string{
			"Tailscale Serve 让你在你的 tailnet 内安全地共享本地服务器。\n",
			"若要在互联网上共享本地服务器，请使用 `tailscale funnel`\n\n",
		}, "\n"),
	},
	funnel: {
		Name:      "funnel",
		ShortHelp: "在互联网上提供服务内容与本地服务器",
		LongHelp: strings.Join([]string{
			"Funnel 让你使用 Tailscale 在互联网上共享本地服务器。\n",
			"若仅在你的 tailnet 内共享，请使用 `tailscale serve`\n\n",
		}, "\n"),
	},
}

// errHelpFunc is standard error text that prompts users to
// run `$subcmd --help` for information on how to use serve.
var errHelpFunc = func(m serveMode) error {
	return fmt.Errorf("请尝试运行 `tailscale %s --help` 查看用法信息", infoMap[m].Name)
}

// newServeV2Command returns a new "serve" subcommand using e as its environment.
func newServeV2Command(e *serveEnv, subcmd serveMode) *ffcli.Command {
	if subcmd != serve && subcmd != funnel {
		log.Fatalf("newServeDevCommand called with unknown subcmd %v", subcmd)
	}

	info := infoMap[subcmd]

	return &ffcli.Command{
		Name:      info.Name,
		ShortHelp: info.ShortHelp,
		ShortUsage: strings.Join([]string{
			fmt.Sprintf("tailscale %s <target>", info.Name),
			fmt.Sprintf("tailscale %s status [--json]", info.Name),
			fmt.Sprintf("tailscale %s reset", info.Name),
		}, "\n"),
		LongHelp: info.LongHelp + fmt.Sprintf(strings.TrimSpace(serveHelpCommon), info.Name),
		Exec:     e.runServeCombined(subcmd),

		FlagSet: e.newFlags("serve-set", func(fs *flag.FlagSet) {
			fs.Var(&e.bg, "bg", "以后台进程方式运行命令（默认为 false，当设置了 --service 时默认为 true）。")
			fs.StringVar(&e.setPath, "set-path", "", "将指定路径追加到用于访问底层服务的基础 URL 之后")
			fs.UintVar(&e.https, "https", 0, "在指定端口暴露一个 HTTPS 服务器（默认模式）")
			if subcmd == serve {
				fs.UintVar(&e.http, "http", 0, "在指定端口暴露一个 HTTP 服务器")
				fs.Var(&acceptAppCapsFlag{Value: &e.acceptAppCaps}, "accept-app-caps", "要转发到服务器的应用能力（使用逗号分隔的列表指定多个能力）")
				fs.Var(&serviceNameFlag{Value: &e.service}, "service", "为具有独立虚拟 IP 的服务提供服务，而不是节点本身。")
				fs.BoolVar(&e.tun, "tun", false, "将所有流量转发到本地机器（默认为 false），仅支持服务于服务。详见文档了解更多信息。")
			}
			fs.UintVar(&e.tcp, "tcp", 0, "暴露一个 TCP 转发器，在指定端口转发原始 TCP 数据包")
			fs.UintVar(&e.tlsTerminatedTCP, "tls-terminated-tcp", 0, "暴露一个 TCP 转发器，在指定端口转发已终止 TLS 的 TCP 数据包")
			fs.UintVar(&e.proxyProtocol, "proxy-protocol", 0, "用于 TCP 转发的 PROXY 协议版本（1 或 2）")
			fs.BoolVar(&e.yes, "yes", false, "无需交互式提示直接更新（默认 false）")
		}),
		UsageFunc: usageFuncNoDefaultValues,
		Subcommands: func() []*ffcli.Command {
			subcmds := []*ffcli.Command{
				{
					Name:       "status",
					ShortUsage: "tailscale " + info.Name + " status [--json]",
					Exec:       e.runServeStatus,
					ShortHelp:  "查看当前 " + info.Name + " 配置",
					FlagSet: e.newFlags("serve-status", func(fs *flag.FlagSet) {
						fs.BoolVar(&e.json, "json", false, "输出 JSON")
					}),
				},
				{
					Name:       "reset",
					ShortUsage: "tailscale " + info.Name + " reset",
					ShortHelp:  "重置当前 " + info.Name + " 配置",
					Exec:       e.runServeReset,
					FlagSet:    e.newFlags("serve-reset", nil),
				},
			}
			if subcmd == serve {
				subcmds = append(subcmds, []*ffcli.Command{
					{
						Name:       "drain",
						ShortUsage: fmt.Sprintf("tailscale %s drain <service>", info.Name),
						ShortHelp:  "Drain a service from the current node",
						LongHelp: "使当前节点不再为指定服务接受新的连接。\n" +
							"已有的连接会继续工作直到它们被关闭，但不会再接受新连接。\n" +
							"使用此命令可在不打断现有连接的情况下优雅地从当前节点移除一个服务。\n" +
							"<service> 应为一个服务名称（如 svc:my-service）。",
						Exec: e.runServeDrain,
					},
					{
						Name:       "clear",
						ShortUsage: fmt.Sprintf("tailscale %s clear <service>", info.Name),
						ShortHelp:  "Remove all config for a service",
						LongHelp:   "移除为指定服务配置的所有处理器。",
						Exec:       e.runServeClear,
					},
					{
						Name:       "advertise",
						ShortUsage: fmt.Sprintf("tailscale %s advertise <service>", info.Name),
						ShortHelp:  "Advertise this node as a service proxy to the tailnet",
						LongHelp: "将当前节点作为服务代理通告给 tailnet。此命令用于\n" +
							"使当前节点被视为某个服务的服务主机。这在服务被 drain 之后\n" +
							"让服务重新上线时很有用（即在运行了\n" +
							"`tailscale serve drain <service>` 之后）。如果你使用 `tailscale serve` 来初始化服务，则无需此命令。",
						Exec: e.runServeAdvertise,
					},
					{
						Name:       "get-config",
						ShortUsage: fmt.Sprintf("tailscale %s get-config <file> [--service=<service>] [--all]", info.Name),
						ShortHelp:  "获取服务配置以保存到文件",
						LongHelp: "以稍后可提供给 set-config 的格式，获取当前节点托管的服务配置。\n" +
							"这可用于声明式地设置服务主机的配置。",
						Exec: e.runServeGetConfig,
						FlagSet: e.newFlags("serve-get-config", func(fs *flag.FlagSet) {
							fs.BoolVar(&e.allServices, "all", false, "read config from all services")
							fs.Var(&serviceNameFlag{Value: &e.service}, "service", "read config from a particular service")
						}),
					},
					{
						Name:       "set-config",
						ShortUsage: fmt.Sprintf("tailscale %s set-config <file> [--service=<service>] [--all]", info.Name),
						ShortHelp:  "Define service configuration from a file",
						LongHelp: "读取提供的配置文件，并使用它声明式地设置配置，\n" +
							"可以针对单个服务，也可以针对当前节点托管的所有服务。如果指定了 --service，\n" +
							"该服务的所有端点处理器都会被覆盖。如果指定了 --all，所有服务的所有端点处理器都会被覆盖。\n\n" +
							"关于文件格式的信息，请参阅 tailscale.com/kb/1589/tailscale-services-configuration-file",
						Exec: e.runServeSetConfig,
						FlagSet: e.newFlags("serve-set-config", func(fs *flag.FlagSet) {
							fs.BoolVar(&e.allServices, "all", false, "apply config to all services")
							fs.Var(&serviceNameFlag{Value: &e.service}, "service", "apply config to a particular service")
						}),
					},
				}...)
			}
			return subcmds
		}(),
	}
}

func (e *serveEnv) validateArgs(subcmd serveMode, args []string) error {
	if translation, ok := isLegacyInvocation(subcmd, args); ok {
		fmt.Fprint(e.stderr(), "错误：serve 和 funnel 的命令行接口已发生变化。")
		if translation != "" {
			fmt.Fprint(e.stderr(), " 你可以改为运行以下命令：\n")
			fmt.Fprintf(e.stderr(), "\t- %s\n", translation)
		}
		fmt.Fprint(e.stderr(), "\n请访问 https://tailscale.com/kb/1242/tailscale-serve 了解更多信息。\n")
		return errHelpFunc(subcmd)
	}
	if len(args) == 0 && e.tun {
		return nil
	}
	if len(args) == 0 {
		return flag.ErrHelp
	}
	if e.tun && len(args) > 1 {
		fmt.Fprintln(e.stderr(), "错误：参数格式无效")
		return errHelpFunc(subcmd)
	}
	if len(args) > 2 {
		fmt.Fprintf(e.stderr(), "错误：参数数量无效（%d）\n", len(args))
		return errHelpFunc(subcmd)
	}
	turnOff := args[len(args)-1] == "off"
	if len(args) == 2 && !turnOff {
		fmt.Fprintln(e.stderr(), "错误：参数格式无效")
		return errHelpFunc(subcmd)
	}

	// Given the two checks above, we can assume there
	// are only 1 or 2 arguments which is valid.
	return nil
}

// runServeCombined is the entry point for the "tailscale {serve,funnel}" commands.
func (e *serveEnv) runServeCombined(subcmd serveMode) execFunc {
	e.subcmd = subcmd

	return func(ctx context.Context, args []string) error {
		// Undocumented debug command (not using ffcli subcommands) to set raw
		// configs from stdin for now (2022-11-13).
		if len(args) == 1 && args[0] == "set-raw" {
			valb, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			sc := new(ipn.ServeConfig)
			if err := json.Unmarshal(valb, sc); err != nil {
				return fmt.Errorf("无效的 JSON：%w", err)
			}
			return e.lc.SetServeConfig(ctx, sc)
		}

		if err := e.validateArgs(subcmd, args); err != nil {
			return err
		}

		ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
		defer cancel()

		forService := e.service != ""
		if !e.bg.IsSet {
			e.bg.Value = forService
		}

		funnel := subcmd == funnel
		if forService && funnel {
			return errors.New("错误：--service 标志在 funnel 下不受支持")
		}

		if funnel {
			// verify node has funnel capabilities
			if err := e.verifyFunnelEnabled(ctx, 443); err != nil {
				return err
			}
		}

		if forService && !e.bg.Value {
			return errors.New("错误：--service 标志仅与后台模式兼容")
		}

		mount, err := cleanURLPath(e.setPath)
		if err != nil {
			return fmt.Errorf("清理挂载点失败：%w", err)
		}

		srvType, srvPort, err := srvTypeAndPortFromFlags(e)
		if err != nil {
			fmt.Fprintf(e.stderr(), "错误：%v\n\n", err)
			return errHelpFunc(subcmd)
		}

		if (srvType == serveTypeHTTP || srvType == serveTypeHTTPS) && e.proxyProtocol != 0 {
			return fmt.Errorf("PROXY 协议仅支持用于 TCP 转发，不支持 HTTP/HTTPS")
		}
		// Validate PROXY protocol version
		if e.proxyProtocol != 0 && e.proxyProtocol != 1 && e.proxyProtocol != 2 {
			return fmt.Errorf("无效的 PROXY 协议版本 %d；必须为 1 或 2", e.proxyProtocol)
		}

		sc, err := e.lc.GetServeConfig(ctx)
		if err != nil {
			return fmt.Errorf("获取 serve 配置出错：%w", err)
		}

		// nil if no config
		if sc == nil {
			sc = new(ipn.ServeConfig)
		}
		st, err := e.getLocalClientStatusWithoutPeers(ctx)
		if err != nil {
			return fmt.Errorf("获取客户端状态出错：%w", err)
		}
		dnsName := strings.TrimSuffix(st.Self.DNSName, ".")
		magicDNSSuffix := st.CurrentTailnet.MagicDNSSuffix

		// set parent serve config to always be persisted
		// at the top level, but a nested config might be
		// the one that gets manipulated depending on
		// foreground or background.
		parentSC := sc

		turnOff := len(args) > 0 && "off" == args[len(args)-1]
		if !turnOff && srvType == serveTypeHTTPS {
			// Running serve with https requires that the tailnet has enabled
			// https cert provisioning. Send users through an interactive flow
			// to enable this if not already done.
			//
			// TODO(sonia,tailscale/corp#10577): The interactive feature flow
			// is behind a control flag. If the tailnet doesn't have the flag
			// on, enableFeatureInteractive will error. For now, we hide that
			// error and maintain the previous behavior (prior to 2023-08-15)
			// of letting them edit the serve config before enabling certs.
			if err := e.enableFeatureInteractive(ctx, "serve", tailcfg.CapabilityHTTPS); err != nil {
				return fmt.Errorf("启用 https 功能出错：%w", err)
			}
		}

		var watcher *local.IPNBusWatcher
		svcName := noService

		if forService {
			svcName = e.service
			dnsName = e.service.String()
		}
		tagged := st.Self.Tags != nil && st.Self.Tags.Len() > 0
		if forService && !tagged && !turnOff {
			return errors.New("服务主机必须是已打标签的节点")
		}
		if !forService && srvType == serveTypeTUN {
			return errors.New("tun 模式仅支持用于服务")
		}
		wantFg := !e.bg.Value && !turnOff
		if wantFg {
			// if foreground mode, create a WatchIPNBus session
			// and use the nested config for all following operations
			// TODO(marwan-at-work): nested-config validations should happen here or previous to this point.
			watcher, err = e.lc.WatchIPNBus(ctx, ipn.NotifyInitialState)
			if err != nil {
				return err
			}
			defer watcher.Close()
			n, err := watcher.Next()
			if err != nil {
				return err
			}
			if n.SessionID == "" {
				return errors.New("缺少 SessionID")
			}
			fsc := &ipn.ServeConfig{}
			mak.Set(&sc.Foreground, n.SessionID, fsc)
			sc = fsc
		}

		var msg string
		if turnOff {
			// only unset serve when trying to unset with type and port flags.
			err = e.unsetServe(sc, dnsName, srvType, srvPort, mount, magicDNSSuffix)
		} else {
			if forService {
				e.addServiceToPrefs(ctx, svcName)
			}
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			if err := e.shouldWarnRemoteDestCompatibility(ctx, target); err != nil {
				return err
			}
			err = e.setServe(sc, dnsName, srvType, srvPort, mount, target, funnel, magicDNSSuffix, e.acceptAppCaps, int(e.proxyProtocol))
			msg = e.messageForPort(sc, st, dnsName, srvType, srvPort)
		}
		if err != nil {
			fmt.Fprintf(e.stderr(), "错误：%v\n\n", err)
			return errHelpFunc(subcmd)
		}

		if err := e.lc.SetServeConfig(ctx, parentSC); err != nil {
			if local.IsPreconditionsFailedError(err) {
				fmt.Fprintln(e.stderr(), "另一个客户端正在修改 serve 配置；请重试。")
			}
			return err
		}

		if msg != "" {
			fmt.Fprintln(e.stdout(), msg)
		}

		if watcher != nil {
			for {
				_, err = watcher.Next()
				if err != nil {
					if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
						return nil
					}
					return err
				}
			}
		}

		return nil
	}
}

func (e *serveEnv) addServiceToPrefs(ctx context.Context, serviceName tailcfg.ServiceName) error {
	prefs, err := e.lc.GetPrefs(ctx)
	if err != nil {
		return fmt.Errorf("获取偏好设置出错：%w", err)
	}
	advertisedServices := prefs.AdvertiseServices
	if slices.Contains(advertisedServices, serviceName.String()) {
		return nil // already advertised
	}
	advertisedServices = append(advertisedServices, serviceName.String())
	_, err = e.lc.EditPrefs(ctx, &ipn.MaskedPrefs{
		AdvertiseServicesSet: true,
		Prefs: ipn.Prefs{
			AdvertiseServices: advertisedServices,
		},
	})
	return err
}

func (e *serveEnv) removeServiceFromPrefs(ctx context.Context, serviceName tailcfg.ServiceName) error {
	prefs, err := e.lc.GetPrefs(ctx)
	if err != nil {
		return fmt.Errorf("获取偏好设置出错：%w", err)
	}
	if len(prefs.AdvertiseServices) == 0 {
		return nil // nothing to remove
	}
	initialLen := len(prefs.AdvertiseServices)
	prefs.AdvertiseServices = slices.DeleteFunc(prefs.AdvertiseServices, func(s string) bool { return s == serviceName.String() })
	if initialLen == len(prefs.AdvertiseServices) {
		return nil // serviceName not advertised
	}
	_, err = e.lc.EditPrefs(ctx, &ipn.MaskedPrefs{
		AdvertiseServicesSet: true,
		Prefs: ipn.Prefs{
			AdvertiseServices: prefs.AdvertiseServices,
		},
	})
	return err
}

func (e *serveEnv) runServeDrain(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errHelp
	}
	if len(args) != 1 {
		fmt.Fprintf(Stderr, "错误：参数数量无效\n\n")
		return errHelp
	}
	svc := args[0]
	svcName := tailcfg.ServiceName(svc)
	if err := svcName.Validate(); err != nil {
		return fmt.Errorf("无效的服务名称：%w", err)
	}
	return e.removeServiceFromPrefs(ctx, svcName)
}

func (e *serveEnv) runServeClear(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errHelp
	}
	if len(args) != 1 {
		fmt.Fprintf(Stderr, "错误：参数数量无效\n\n")
		return errHelp
	}
	svc := tailcfg.ServiceName(args[0])
	if err := svc.Validate(); err != nil {
		return fmt.Errorf("无效的服务名称：%w", err)
	}
	sc, err := e.lc.GetServeConfig(ctx)
	if err != nil {
		return fmt.Errorf("获取 serve 配置出错：%w", err)
	}
	if _, ok := sc.Services[svc]; !ok {
		log.Printf("serve 配置中未找到服务 %s，无需清理", svc)
		return nil
	}
	delete(sc.Services, svc)
	if err := e.removeServiceFromPrefs(ctx, svc); err != nil {
		return fmt.Errorf("从偏好设置中移除服务 %s 出错：%w", svc, err)
	}
	return e.lc.SetServeConfig(ctx, sc)
}

func (e *serveEnv) runServeAdvertise(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("错误：缺少服务名称参数")
	}
	if len(args) != 1 {
		fmt.Fprintf(Stderr, "错误：参数数量无效\n\n")
		return errHelp
	}
	svc := tailcfg.ServiceName(args[0])
	if err := svc.Validate(); err != nil {
		return fmt.Errorf("无效的服务名称：%w", err)
	}
	return e.addServiceToPrefs(ctx, svc)
}

func (e *serveEnv) runServeGetConfig(ctx context.Context, args []string) (err error) {
	forSingleService := e.service.Validate() == nil
	sc, err := e.lc.GetServeConfig(ctx)
	if err != nil {
		return err
	}

	prefs, err := e.lc.GetPrefs(ctx)
	if err != nil {
		return err
	}
	advertised := set.SetOf(prefs.AdvertiseServices)

	st, err := e.getLocalClientStatusWithoutPeers(ctx)
	if err != nil {
		return err
	}
	magicDNSSuffix := st.CurrentTailnet.MagicDNSSuffix

	handleService := func(svcName tailcfg.ServiceName, serviceConfig *ipn.ServiceConfig) (*conffile.ServiceDetailsFile, error) {
		var sdf conffile.ServiceDetailsFile
		// Leave unset for true case since that's the default.
		if !advertised.Contains(svcName.String()) {
			sdf.Advertised.Set(false)
		}

		if serviceConfig.Tun {
			mak.Set(&sdf.Endpoints, &tailcfg.ProtoPortRange{Ports: tailcfg.PortRangeAny}, &conffile.Target{
				Protocol:         conffile.ProtoTUN,
				Destination:      "",
				DestinationPorts: tailcfg.PortRange{},
			})
		}

		for port, config := range serviceConfig.TCP {
			sniName := fmt.Sprintf("%s.%s", svcName.WithoutPrefix(), magicDNSSuffix)
			ppr := tailcfg.ProtoPortRange{Proto: int(ipproto.TCP), Ports: tailcfg.PortRange{First: port, Last: port}}
			if config.TCPForward != "" {
				var proto conffile.ServiceProtocol
				if config.TerminateTLS != "" {
					proto = conffile.ProtoTLSTerminatedTCP
				} else {
					proto = conffile.ProtoTCP
				}
				destHost, destPortStr, err := net.SplitHostPort(config.TCPForward)
				if err != nil {
					return nil, fmt.Errorf("解析 TCPForward=%q 出错：%w", config.TCPForward, err)
				}
				destPort, err := strconv.ParseUint(destPortStr, 10, 16)
				if err != nil {
					return nil, fmt.Errorf("解析端口 %q 出错：%w", destPortStr, err)
				}
				mak.Set(&sdf.Endpoints, &ppr, &conffile.Target{
					Protocol:         proto,
					Destination:      destHost,
					DestinationPorts: tailcfg.PortRange{First: uint16(destPort), Last: uint16(destPort)},
				})
			} else if config.HTTP || config.HTTPS {
				webKey := ipn.HostPort(net.JoinHostPort(sniName, strconv.FormatUint(uint64(port), 10)))
				handlers, ok := serviceConfig.Web[webKey]
				if !ok {
					return nil, fmt.Errorf("服务 %q：已设置 HTTP/HTTPS，但配置中没有处理器", svcName)
				}
				defaultHandler, ok := handlers.Handlers["/"]
				if !ok {
					return nil, fmt.Errorf("服务 %q：未设置根处理器", svcName)
				}
				if defaultHandler.Path != "" {
					mak.Set(&sdf.Endpoints, &ppr, &conffile.Target{
						Protocol:         conffile.ProtoFile,
						Destination:      defaultHandler.Path,
						DestinationPorts: tailcfg.PortRange{},
					})
				} else if defaultHandler.Proxy != "" {
					proto, rest, ok := strings.Cut(defaultHandler.Proxy, "://")
					if !ok {
						return nil, fmt.Errorf("服务 %q：无效的代理处理器 %q", svcName, defaultHandler.Proxy)
					}
					host, portStr, err := net.SplitHostPort(rest)
					if err != nil {
						return nil, fmt.Errorf("服务 %q：无效的代理处理器 %q：%w", svcName, defaultHandler.Proxy, err)
					}

					port, err := strconv.ParseUint(portStr, 10, 16)
					if err != nil {
						return nil, fmt.Errorf("服务 %q：解析端口 %q 出错：%w", svcName, portStr, err)
					}

					mak.Set(&sdf.Endpoints, &ppr, &conffile.Target{
						Protocol:         conffile.ServiceProtocol(proto),
						Destination:      host,
						DestinationPorts: tailcfg.PortRange{First: uint16(port), Last: uint16(port)},
					})
				}
			}
		}

		return &sdf, nil
	}

	var j []byte

	if e.allServices && forSingleService {
		return errors.New("不能同时指定 --all 和 --service")
	} else if e.allServices {
		var scf conffile.ServicesConfigFile
		scf.Version = "0.0.1"
		for svcName, serviceConfig := range sc.Services {
			sdf, err := handleService(svcName, serviceConfig)
			if err != nil {
				return err
			}
			mak.Set(&scf.Services, svcName, sdf)
		}
		j, err = json.MarshalIndent(scf, "", "  ")
		if err != nil {
			return err
		}
	} else if forSingleService {
		serviceConfig, ok := sc.Services[e.service]
		if !ok {
			j = []byte("{}")
		} else {
			sdf, err := handleService(e.service, serviceConfig)
			if err != nil {
				return err
			}
			sdf.Version = "0.0.1"
			j, err = json.MarshalIndent(sdf, "", "  ")
			if err != nil {
				return err
			}
		}
	} else {
		return errors.New("必须指定 --service=svc:<服务名> 或 --all 其中之一")
	}

	j = append(j, '\n')
	_, err = e.stdout().Write(j)
	return err
}

// serveConfigDocsURL documents the Services configuration file format that set-config prefers
const serveConfigDocsURL = "https://tailscale.com/kb/1589/tailscale-services-configuration-file"

const serveLegacyFormatWarning = "警告：%q 是旧版原始 serve 配置格式 " +
	"（由 `tailscale serve status --json` 输出），set-config 已弃用该格式。" +
	"仅应用其中的服务部分。如需迁移，请运行 `tailscale serve get-config` 以受支持的格式" +
	"保存你的配置；详见 %s\n"

const serveLegacyDroppedWarning = "警告：忽略 set-config 不支持的节点级字段：%s\n"

// legacyNodeLevelFields returns the names of the populated top-level fields in
// sc, other than Services, that set-config does not apply (it is services-only).
func legacyNodeLevelFields(sc *ipn.ServeConfig) []string {
	rest := sc.Clone()
	rest.Services = nil
	b, err := json.Marshal(rest)
	if err != nil {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	fields := make([]string, 0, len(m))
	for k := range m {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	return fields
}

func (e *serveEnv) runServeSetConfig(ctx context.Context, args []string) (err error) {
	if len(args) != 1 {
		return errors.New("必须指定文件名")
	}
	filename := args[0]
	forSingleService := e.service.Validate() == nil
	if e.allServices && forSingleService {
		return errors.New("不能同时指定 --all 和 --service")
	}
	if !e.allServices && !forSingleService {
		return errors.New("必须指定 --service=svc:<服务名> 或 --all 其中之一")
	}

	forService := ""
	if forSingleService {
		forService = e.service.String()
	}
	scf, err := conffile.LoadServicesConfig(filename, forService)
	if err != nil {
		return fmt.Errorf("无法从文件 %q 读取配置：%w", filename, err)
	}

	st, err := e.getLocalClientStatusWithoutPeers(ctx)
	if err != nil {
		return fmt.Errorf("获取客户端状态出错：%w", err)
	}
	magicDNSSuffix := st.CurrentTailnet.MagicDNSSuffix
	sc, err := e.lc.GetServeConfig(ctx)
	if err != nil {
		return fmt.Errorf("获取当前 serve 配置出错：%w", err)
	}

	// Clear all existing config.
	if forSingleService {
		if sc.Services != nil {
			if sc.Services[e.service] != nil {
				delete(sc.Services, e.service)
			}
		}
	} else {
		sc.Services = map[tailcfg.ServiceName]*ipn.ServiceConfig{}
	}
	advertisedServices := set.Set[string]{}

	if scf.Version == conffile.LegacyVersion {
		// Legacy raw ipn.ServeConfig (e.g. "tailscale serve status --json"
		// output). Deprecated for set-config; apply only its services-oriented
		// content, with a migration warning to stderr (never stdout, which
		// callers may pipe).
		legacy := scf.Legacy
		fmt.Fprintf(e.stderr(), serveLegacyFormatWarning, filename, serveConfigDocsURL)
		if dropped := legacyNodeLevelFields(legacy); len(dropped) > 0 {
			fmt.Fprintf(e.stderr(), serveLegacyDroppedWarning, strings.Join(dropped, ", "))
		}
		for name, svcCfg := range legacy.Services {
			if forSingleService && name != e.service {
				continue
			}
			mak.Set(&sc.Services, name, svcCfg.Clone())
			advertisedServices.Add(name.String())
		}
		if forSingleService && sc.Services[e.service] == nil {
			return fmt.Errorf("在 %q 中未找到服务 %q", filename, e.service)
		}
	}

	// scf.Services is nil for the legacy format, making this loop a no-op then.
	for name, details := range scf.Services {
		for ppr, ep := range details.Endpoints {
			if ep.Protocol == conffile.ProtoTUN {
				err := e.setServe(sc, name.String(), serveTypeTUN, 0, "", "", false, magicDNSSuffix, nil, 0 /* proxy protocol */)
				if err != nil {
					return err
				}
				// TUN mode is exclusive.
				break
			}

			if ppr.Proto != int(ipproto.TCP) {
				return fmt.Errorf("服务 %q：源端口必须是 TCP", name)
			}
			serveType, _ := serveTypeFromConfString(ep.Protocol)
			for port := ppr.Ports.First; port <= ppr.Ports.Last; port++ {
				var target string
				if ep.Protocol == conffile.ProtoFile {
					target = ep.Destination
				} else {
					// map source port range 1-1 to destination port range
					destPort := ep.DestinationPorts.First + (port - ppr.Ports.First)
					portStr := fmt.Sprint(destPort)
					target = fmt.Sprintf("%s://%s", ep.Protocol, net.JoinHostPort(ep.Destination, portStr))
				}
				err := e.setServe(sc, name.String(), serveType, port, "/", target, false, magicDNSSuffix, nil, 0 /* proxy protocol */)
				if err != nil {
					return fmt.Errorf("服务 %q：%w", name, err)
				}
			}
		}
		if v, set := details.Advertised.Get(); !set || v {
			advertisedServices.Add(name.String())
		}
	}

	var changed bool
	var servicesList []string
	if e.allServices {
		servicesList = advertisedServices.Slice()
		changed = true
	} else if advertisedServices.Contains(e.service.String()) {
		// If allServices wasn't set, the only service that could have been
		// advertised is the one that was provided as a flag.
		prefs, err := e.lc.GetPrefs(ctx)
		if err != nil {
			return err
		}
		if !slices.Contains(prefs.AdvertiseServices, e.service.String()) {
			servicesList = append(prefs.AdvertiseServices, e.service.String())
			changed = true
		}
	}
	if changed {
		_, err = e.lc.EditPrefs(ctx, &ipn.MaskedPrefs{
			AdvertiseServicesSet: true,
			Prefs: ipn.Prefs{
				AdvertiseServices: servicesList,
			},
		})
		if err != nil {
			return err
		}
	}

	return e.lc.SetServeConfig(ctx, sc)
}

func (e *serveEnv) setServe(sc *ipn.ServeConfig, dnsName string, srvType serveType, srvPort uint16, mount string, target string, allowFunnel bool, mds string, caps []tailcfg.PeerCapability, proxyProtocol int) error {
	// update serve config based on the type
	switch srvType {
	case serveTypeHTTPS, serveTypeHTTP:
		useTLS := srvType == serveTypeHTTPS
		err := e.applyWebServe(sc, dnsName, srvPort, useTLS, mount, target, mds, caps)
		if err != nil {
			return fmt.Errorf("failed apply web serve: %w", err)
		}
	case serveTypeTCP, serveTypeTLSTerminatedTCP:
		if e.setPath != "" {
			return fmt.Errorf("TCP serve 无法挂载路径")
		}
		err := e.applyTCPServe(sc, dnsName, srvType, srvPort, target, mds, proxyProtocol)
		if err != nil {
			return fmt.Errorf("应用 TCP serve 失败：%w", err)
		}
	case serveTypeTUN:
		// Caller checks that TUN mode is only supported for services.
		svcName := tailcfg.ServiceName(dnsName)
		if _, ok := sc.Services[svcName]; !ok {
			mak.Set(&sc.Services, svcName, new(ipn.ServiceConfig))
		}
		sc.Services[svcName].Tun = true
	default:
		return fmt.Errorf("无效的类型 %q", srvType)
	}

	// update the serve config based on if funnel is enabled
	// Since funnel is not supported for services, we only apply it for node's serve.
	if svcName := tailcfg.AsServiceName(dnsName); svcName == noService {
		e.applyFunnel(sc, dnsName, srvPort, allowFunnel)
	}
	return nil
}

var (
	msgFunnelAvailable             = "可在互联网上访问："
	msgServeAvailable              = "可在你的 tailnet 内访问："
	msgServiceWaitingApproval      = "本机已配置为 %s 的服务代理，但需要管理员批准。一旦获批，它将在你的 Tailnet 中以如下地址可用："
	msgRunningInBackground         = "%s 已启动并在后台运行。"
	msgRunningTunService           = "到 %s 的 IPv4 和 IPv6 流量正被路由到你的操作系统。"
	msgDisableProxy                = "要禁用代理，运行：tailscale %s --%s=%d off"
	msgDisableServiceProxy         = "要禁用代理，运行：tailscale serve --service=%s --%s=%d off"
	msgDisableServiceTun           = "要禁用 TUN 模式下的服务，运行：tailscale serve --service=%s --tun off"
	msgDisableService              = "要移除该服务的配置，运行：tailscale serve clear %s"
	msgWarnRemoteDestCompatibility = "警告：%s 不支持通过非默认路由连接到远程目标，详见 tailscale.com/kb/1552/tailscale-services。"
	msgToExit                      = "按 Ctrl+C 退出。"
)

// messageForPort returns a message for the given port based on the
// serve config and status.
func (e *serveEnv) messageForPort(sc *ipn.ServeConfig, st *ipnstate.Status, dnsName string, srvType serveType, srvPort uint16) string {
	var output strings.Builder
	svcName := tailcfg.AsServiceName(dnsName)
	forService := svcName != noService
	var webConfig *ipn.WebServerConfig
	var tcpHandler *ipn.TCPPortHandler
	ips := st.TailscaleIPs
	magicDNSSuffix := st.CurrentTailnet.MagicDNSSuffix
	host := dnsName
	if forService {
		host = strings.Join([]string{svcName.WithoutPrefix(), magicDNSSuffix}, ".")
	}
	hp := ipn.HostPort(net.JoinHostPort(host, strconv.Itoa(int(srvPort))))

	scheme := "https"
	if sc.IsServingHTTP(srvPort, svcName) {
		scheme = "http"
	}

	portPart := ":" + fmt.Sprint(srvPort)
	if scheme == "http" && srvPort == 80 ||
		scheme == "https" && srvPort == 443 {
		portPart = ""
	}

	srvTypeAndDesc := func(h *ipn.HTTPHandler) (string, string) {
		switch {
		case h.Path != "":
			return "path", h.Path
		case h.Proxy != "":
			return "proxy", h.Proxy
		case h.Text != "":
			return "text", "\"" + elipticallyTruncate(h.Text, 20) + "\""
		}
		return "", ""
	}
	if forService {
		serviceIPMaps, err := tailcfg.UnmarshalNodeCapJSON[tailcfg.ServiceIPMappings](st.Self.CapMap, tailcfg.NodeAttrServiceHost)
		if err != nil || len(serviceIPMaps) == 0 || serviceIPMaps[0][svcName] == nil {
			// The capmap does not contain IPs for this service yet. Usually this means
			// the service hasn't been added to prefs and sent to control yet.
			output.WriteString(fmt.Sprintf(msgServiceWaitingApproval, svcName.String()))
			ips = nil
		} else {
			output.WriteString(msgServeAvailable)
			ips = serviceIPMaps[0][svcName]
		}
		output.WriteString("\n\n")
		svc := sc.Services[svcName]
		if srvType == serveTypeTUN && svc.Tun {
			output.WriteString(fmt.Sprintf(msgRunningTunService, host))
			output.WriteString("\n")
			output.WriteString(fmt.Sprintf(msgDisableServiceTun, dnsName))
			output.WriteString("\n")
			output.WriteString(fmt.Sprintf(msgDisableService, dnsName))
			return output.String()
		}
		if svc != nil {
			webConfig = svc.Web[hp]
			tcpHandler = svc.TCP[srvPort]
		}
	} else {
		if sc.AllowFunnel[hp] == true {
			output.WriteString(msgFunnelAvailable)
		} else {
			output.WriteString(msgServeAvailable)
		}
		output.WriteString("\n\n")
		webConfig = sc.Web[hp]
		tcpHandler = sc.TCP[srvPort]
	}

	if webConfig != nil {
		mounts := slicesx.MapKeys(webConfig.Handlers)
		sort.Slice(mounts, func(i, j int) bool {
			return len(mounts[i]) < len(mounts[j])
		})
		for _, m := range mounts {
			t, d := srvTypeAndDesc(webConfig.Handlers[m])
			output.WriteString(fmt.Sprintf("%s://%s%s%s\n", scheme, host, portPart, m))
			output.WriteString(fmt.Sprintf("%s %-5s %s\n\n", "|--", t, d))
		}
	} else if tcpHandler != nil {

		tlsStatus := "TLS over TCP"
		if tcpHandler.TerminateTLS != "" {
			tlsStatus = "TLS terminated"
		}
		if ver := tcpHandler.ProxyProtocol; ver != 0 {
			tlsStatus = fmt.Sprintf("%s, PROXY protocol v%d", tlsStatus, ver)
		}

		output.WriteString(fmt.Sprintf("|-- tcp://%s:%d (%s)\n", host, srvPort, tlsStatus))
		for _, a := range ips {
			ipp := net.JoinHostPort(a.String(), strconv.Itoa(int(srvPort)))
			output.WriteString(fmt.Sprintf("|-- tcp://%s\n", ipp))
		}
		output.WriteString(fmt.Sprintf("|--> tcp://%s\n\n", tcpHandler.TCPForward))
	}

	if !forService && !e.bg.Value {
		output.WriteString(msgToExit)
		return output.String()
	}

	subCmd := infoMap[e.subcmd].Name
	subCmdUpper := strings.ToUpper(string(subCmd[0])) + subCmd[1:]

	output.WriteString(fmt.Sprintf(msgRunningInBackground, subCmdUpper))
	output.WriteString("\n")
	if forService {
		output.WriteString(fmt.Sprintf(msgDisableServiceProxy, dnsName, srvType.String(), srvPort))
		output.WriteString("\n")
		output.WriteString(fmt.Sprintf(msgDisableService, dnsName))
	} else {
		output.WriteString(fmt.Sprintf(msgDisableProxy, subCmd, srvType.String(), srvPort))
	}

	return output.String()
}

// isRemote reports whether the given destination from serve config
// is a remote destination.
func isRemote(target string) bool {
	// target being a port number means it's localhost
	if _, err := strconv.ParseUint(target, 10, 16); err == nil {
		return false
	}

	// prepend tmp:// if no scheme is present just to help parsing
	if !strings.Contains(target, "://") {
		target = "tmp://" + target
	}

	// make sure we can parse the target, whether it's a full URL or just a host:port
	u, err := url.ParseRequestURI(target)
	if err != nil {
		// If we can't parse the target, it doesn't matter if it's remote or not
		return false
	}
	validHN := dnsname.ValidHostname(u.Hostname()) == nil
	validIP := net.ParseIP(u.Hostname()) != nil
	if !validHN && !validIP {
		return false
	}
	if u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" {
		return false
	}
	return true
}

// shouldWarnRemoteDestCompatibility reports whether we should warn the user
// that their current OS/environment may not be compatible with
// service's proxy destination.
func (e *serveEnv) shouldWarnRemoteDestCompatibility(ctx context.Context, target string) error {
	// no target means nothing to check
	if target == "" {
		return nil
	}

	if filepath.IsAbs(target) || strings.HasPrefix(target, "text:") {
		// local path or text target, nothing to check
		return nil
	}

	// only check for remote destinations
	if !isRemote(target) {
		return nil
	}

	// Check if running as Mac extension and warn
	if version.IsMacAppStore() || version.IsMacSysExt() {
		return fmt.Errorf(msgWarnRemoteDestCompatibility, "the MacOS extension")
	}

	// Check for linux, if it's running with TS_FORCE_LINUX_BIND_TO_DEVICE=true
	// and tailscale bypass mark is not working. If any of these conditions are true, and the dest is
	// a remote destination, return true.
	if runtime.GOOS == "linux" {
		SOMarkInUse, err := e.lc.CheckSOMarkInUse(ctx)
		if err != nil {
			log.Printf("error checking SO mark in use: %v", err)
			return nil
		}
		if !SOMarkInUse {
			return fmt.Errorf(msgWarnRemoteDestCompatibility, "the Linux tailscaled without SO_MARK")
		}
	}

	return nil
}

func (e *serveEnv) applyWebServe(sc *ipn.ServeConfig, dnsName string, srvPort uint16, useTLS bool, mount, target, mds string, caps []tailcfg.PeerCapability) error {
	h := new(ipn.HTTPHandler)
	switch {
	case strings.HasPrefix(target, "text:"):
		text := strings.TrimPrefix(target, "text:")
		if text == "" {
			return errors.New("无法提供内容；文本不能为空字符串")
		}
		h.Text = text
	case filepath.IsAbs(target):
		if version.IsMacAppStore() || version.IsMacSys() {
			// The Tailscale network extension cannot serve arbitrary paths on macOS due to sandbox restrictions (2024-03-26)
			return errors.New("由于沙盒限制，macOS 上不支持路径服务。若要在 macOS 上使用 Tailscale Serve，请切换到开源的 tailscaled 发行版。详见 https://tailscale.com/kb/1065/macos-variants。")
		}

		target = filepath.Clean(target)
		fi, err := os.Stat(target)
		if err != nil {
			return errors.New("无效路径")
		}

		// TODO: need to understand this further
		if fi.IsDir() && !strings.HasSuffix(mount, "/") {
			// dir mount points must end in /
			// for relative file links to work
			mount += "/"
		}
		h.Path = target
	default:
		// Include unix in supported schemes for HTTP(S) serve
		t, err := ipn.ExpandProxyTargetValue(target, []string{"http", "https", "https+insecure", "unix"}, "http")
		if err != nil {
			return err
		}
		h.Proxy = t
		h.AcceptAppCaps = caps
	}

	// TODO: validation needs to check nested foreground configs
	svcName := tailcfg.AsServiceName(dnsName)
	if sc.IsTCPForwardingOnPort(srvPort, svcName) {
		return errors.New("无法提供 Web 服务；已经在提供 TCP 服务")
	}

	sc.SetWebHandler(h, dnsName, srvPort, mount, useTLS, mds)

	return nil
}

func (e *serveEnv) applyTCPServe(sc *ipn.ServeConfig, dnsName string, srcType serveType, srcPort uint16, target string, mds string, proxyProtocol int) error {
	var terminateTLS bool
	switch srcType {
	case serveTypeTCP:
		terminateTLS = false
	case serveTypeTLSTerminatedTCP:
		terminateTLS = true
	default:
		return fmt.Errorf("无效的 TCP 目标 %q", target)
	}

	svcName := tailcfg.AsServiceName(dnsName)

	targetURL, err := ipn.ExpandProxyTargetValue(target, []string{"tcp"}, "tcp")
	if err != nil {
		return fmt.Errorf("无法展开目标：%v", err)
	}

	dstURL, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("无效的 TCP 目标 %q：%v", target, err)
	}

	if sc.IsServingWeb(srcPort, svcName) {
		return fmt.Errorf("无法提供 TCP 服务；已在为 %s 的端口 %d 提供 Web 服务", dnsName, srcPort)
	}

	// TODO: needs to account for multiple configs from foreground mode
	if svcName := tailcfg.AsServiceName(dnsName); svcName != "" {
		sc.SetTCPForwardingForService(srcPort, dstURL.Host, terminateTLS, svcName, proxyProtocol, mds)
		return nil
	}

	// TODO: needs to account for multiple configs from foreground mode
	if svcName != "" {
		sc.SetTCPForwardingForService(srcPort, dstURL.Host, terminateTLS, svcName, proxyProtocol, mds)
		return nil
	}

	sc.SetTCPForwarding(srcPort, dstURL.Host, terminateTLS, proxyProtocol, dnsName)
	return nil
}

func (e *serveEnv) applyFunnel(sc *ipn.ServeConfig, dnsName string, srvPort uint16, allowFunnel bool) {
	hp := ipn.HostPort(net.JoinHostPort(dnsName, strconv.Itoa(int(srvPort))))

	// TODO: Should we return an error? Should not be possible.
	// nil if no config
	if sc == nil {
		sc = new(ipn.ServeConfig)
	}

	if _, exists := sc.AllowFunnel[hp]; exists && !allowFunnel {
		fmt.Fprintf(e.stderr(), "正在为 %s:%s 移除 Funnel\n", dnsName, hp)
	}
	sc.SetFunnel(dnsName, srvPort, allowFunnel)
}

// unsetServe removes the serve config for the given serve port.
// dnsName is a FQDN or a serviceName (with `svc:` prefix). mds
// is the Magic DNS suffix, which is used to recreate serve's host.
func (e *serveEnv) unsetServe(sc *ipn.ServeConfig, dnsName string, srvType serveType, srvPort uint16, mount string, mds string) error {
	switch srvType {
	case serveTypeHTTPS, serveTypeHTTP:
		err := e.removeWebServe(sc, dnsName, srvPort, mount, mds)
		if err != nil {
			return fmt.Errorf("移除 Web serve 失败：%w", err)
		}
	case serveTypeTCP, serveTypeTLSTerminatedTCP:
		err := e.removeTCPServe(sc, dnsName, srvPort)
		if err != nil {
			return fmt.Errorf("移除 TCP serve 失败：%w", err)
		}
	case serveTypeTUN:
		err := e.removeTunServe(sc, dnsName)
		if err != nil {
			return fmt.Errorf("移除 TUN serve 失败：%w", err)
		}
	default:
		return fmt.Errorf("无效的类型 %q", srvType)
	}

	// TODO(tylersmalley): remove funnel

	return nil
}

func srvTypeAndPortFromFlags(e *serveEnv) (srvType serveType, srvPort uint16, err error) {
	sourceMap := map[serveType]uint{
		serveTypeHTTP:             e.http,
		serveTypeHTTPS:            e.https,
		serveTypeTCP:              e.tcp,
		serveTypeTLSTerminatedTCP: e.tlsTerminatedTCP,
	}

	var srcTypeCount int

	for k, v := range sourceMap {
		if v != 0 {
			if v > math.MaxUint16 {
				return 0, 0, fmt.Errorf("端口号 %d 对于 %s 标志来说过大", v, srvType)
			}
			srcTypeCount++
			srvType = k
			srvPort = uint16(v)
		}
	}

	if e.tun {
		srcTypeCount++
		srvType = serveTypeTUN
	}

	if srcTypeCount > 1 {
		return 0, 0, fmt.Errorf("无法为单个挂载点提供多种类型")
	}
	if srcTypeCount == 0 {
		return serveTypeHTTPS, 443, nil
	}

	return srvType, srvPort, nil
}

// isLegacyInvocation helps transition customers who have been using the beta
// CLI to the newer API by returning a translation from the old command to the new command.
// The second result is a boolean that only returns true if the given arguments is a valid
// legacy invocation. If the given args are in the old format but are not valid, it will
// return false and expects the new code path has enough validations to reject the request.
func isLegacyInvocation(subcmd serveMode, args []string) (string, bool) {
	if subcmd == funnel {
		if len(args) != 2 {
			return "", false
		}
		_, err := strconv.ParseUint(args[0], 10, 16)
		return "", err == nil && (args[1] == "on" || args[1] == "off")
	}
	turnOff := len(args) > 1 && args[len(args)-1] == "off"
	if turnOff {
		args = args[:len(args)-1]
	}
	if len(args) == 0 {
		return "", false
	}

	srcType, srcPortStr, found := strings.Cut(args[0], ":")
	if !found {
		if srcType == "https" && srcPortStr == "" {
			// Default https port to 443.
			srcPortStr = "443"
		} else if srcType == "http" && srcPortStr == "" {
			// Default http port to 80.
			srcPortStr = "80"
		} else {
			return "", false
		}
	}

	var wantLength int
	switch srcType {
	case "https", "http":
		wantLength = 3
	case "tcp", "tls-terminated-tcp":
		wantLength = 2
	default:
		// return non-legacy, and let new code handle validation.
		return "", false
	}
	// The length is either exactlly the same as in "https / <target>"
	// or target is omitted as in "https / off" where omit the off at
	// the top.
	if len(args) != wantLength && !(turnOff && len(args) == wantLength-1) {
		return "", false
	}

	cmd := []string{"tailscale", "serve", "--bg"}
	switch srcType {
	case "https":
		// In the new code, we default to https:443,
		// so we don't need to pass the flag explicitly.
		if srcPortStr != "443" {
			cmd = append(cmd, fmt.Sprintf("--https %s", srcPortStr))
		}
	case "http":
		cmd = append(cmd, fmt.Sprintf("--http %s", srcPortStr))
	case "tcp", "tls-terminated-tcp":
		cmd = append(cmd, fmt.Sprintf("--%s %s", srcType, srcPortStr))
	}

	var mount string
	if srcType == "https" || srcType == "http" {
		mount = args[1]
		if _, err := cleanMountPoint(mount); err != nil {
			return "", false
		}
		if mount != "/" {
			cmd = append(cmd, "--set-path "+mount)
		}
	}

	// If there's no "off" there must always be a target destination.
	// If there is "off", target is optional so check if it exists
	// first before appending it.
	hasTarget := !turnOff || (turnOff && len(args) == wantLength)
	if hasTarget {
		dest := args[len(args)-1]
		if strings.Contains(dest, " ") {
			dest = strconv.Quote(dest)
		}
		cmd = append(cmd, dest)
	}
	if turnOff {
		cmd = append(cmd, "off")
	}

	return strings.Join(cmd, " "), true
}

// removeWebServe removes a web handler from the serve config
// and removes funnel if no remaining mounts exist for the serve port.
// The srvPort argument is the serving port and the mount argument is
// the mount point or registered path to remove. mds is the Magic DNS suffix,
// which is used to recreate serve's host.
func (e *serveEnv) removeWebServe(sc *ipn.ServeConfig, dnsName string, srvPort uint16, mount string, mds string) error {
	if sc == nil {
		return nil
	}

	portStr := strconv.Itoa(int(srvPort))
	hostName := dnsName
	webServeMap := sc.Web
	svcName := tailcfg.AsServiceName(dnsName)
	forService := svcName != noService
	if forService {
		svc := sc.Services[svcName]
		if svc == nil {
			return errors.New("服务不存在")
		}
		hostName = strings.Join([]string{svcName.WithoutPrefix(), mds}, ".")
		webServeMap = svc.Web
	}

	hp := ipn.HostPort(net.JoinHostPort(hostName, portStr))

	if sc.IsTCPForwardingOnPort(srvPort, svcName) {
		return errors.New("无法移除 Web 处理器；当前正在提供 TCP 服务")
	}
	var targetExists bool
	var mounts []string
	// mount is deduced from e.setPath but it is ambiguous as
	// to whether the user explicitly passed "/" or it was defaulted to.
	if e.setPath == "" {
		targetExists = webServeMap[hp] != nil && len(webServeMap[hp].Handlers) > 0
		if targetExists {
			for mount := range webServeMap[hp].Handlers {
				mounts = append(mounts, mount)
			}
		}
	} else {
		targetExists = sc.WebHandlerExists(svcName, hp, mount)
		mounts = []string{mount}
	}

	if !targetExists {
		return errors.New("处理器不存在")
	}

	if len(mounts) > 1 {
		msg := fmt.Sprintf("确定要删除端口 %s 下的 %d 个处理器吗？", portStr, len(mounts))
		if !e.yes && !prompt.YesNo(msg, true) {
			return nil
		}
	}

	if forService {
		sc.RemoveServiceWebHandler(svcName, hostName, srvPort, mounts)
	} else {
		sc.RemoveWebHandler(dnsName, srvPort, mounts, true)
	}
	return nil
}

// removeTCPServe removes the TCP forwarding configuration for the
// given srvPort, or serving port for the given dnsName.
func (e *serveEnv) removeTCPServe(sc *ipn.ServeConfig, dnsName string, src uint16) error {
	if sc == nil {
		return nil
	}
	svcName := tailcfg.AsServiceName(dnsName)
	if sc.GetTCPPortHandler(src, svcName) == nil {
		return errors.New("serve 配置不存在")
	}
	if sc.IsServingWeb(src, svcName) {
		return fmt.Errorf("无法移除；正在提供 Web 服务，而非端口 %d 上的 TCP 转发", src)
	}
	sc.RemoveTCPForwarding(svcName, src)
	return nil
}

func (e *serveEnv) removeTunServe(sc *ipn.ServeConfig, dnsName string) error {
	if sc == nil {
		return nil
	}
	svcName := tailcfg.ServiceName(dnsName)
	svc, ok := sc.Services[svcName]
	if !ok || svc == nil {
		return errors.New("服务不存在")
	}
	if !svc.Tun {
		return errors.New("该服务未在 TUN 模式下提供服务")
	}
	delete(sc.Services, svcName)
	if len(sc.Services) == 0 {
		sc.Services = nil // clean up empty map
	}
	return nil
}

// cleanURLPath ensures the path is clean and has a leading "/".
func cleanURLPath(urlPath string) (string, error) {
	if urlPath == "" {
		return "/", nil
	}

	// TODO(tylersmalley) verify still needed with path being a flag
	urlPath = cleanMinGWPathConversionIfNeeded(urlPath)
	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}

	c := path.Clean(urlPath)
	if urlPath == c || urlPath == c+"/" {
		return urlPath, nil
	}
	return "", fmt.Errorf("无效的挂载点 %q", urlPath)
}

func (s serveType) String() string {
	switch s {
	case serveTypeHTTP:
		return "http"
	case serveTypeHTTPS:
		return "https"
	case serveTypeTCP:
		return "tcp"
	case serveTypeTLSTerminatedTCP:
		return "tls-terminated-tcp"
	default:
		return "unknownServeType"
	}
}

func (e *serveEnv) stdout() io.Writer {
	if e.testStdout != nil {
		return e.testStdout
	}
	return Stdout
}

func (e *serveEnv) stderr() io.Writer {
	if e.testStderr != nil {
		return e.testStderr
	}
	return Stderr
}
