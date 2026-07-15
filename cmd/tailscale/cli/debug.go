// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/control/ts2021"
	"tailscale.com/feature"
	_ "tailscale.com/feature/condregister/useproxy"
	"tailscale.com/health"
	"tailscale.com/hostinfo"
	"tailscale.com/ipn"
	"tailscale.com/net/ace"
	"tailscale.com/net/dnscache"
	"tailscale.com/net/netmon"
	"tailscale.com/net/netutil"
	"tailscale.com/net/tsaddr"
	"tailscale.com/net/tsdial"
	"tailscale.com/paths"
	"tailscale.com/safesocket"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
	"tailscale.com/util/eventbus"
	"tailscale.com/util/must"
)

var (
	debugCaptureCmd          func() *ffcli.Command // or nil
	debugPortmapCmd          func() *ffcli.Command // or nil
	debugPeerRelayCmd        func() *ffcli.Command // or nil
	debugClearNetmapCacheCmd func() *ffcli.Command // or nil
)

func debugCmd() *ffcli.Command {
	return &ffcli.Command{
		Name:       "debug",
		Exec:       runDebug,
		ShortUsage: "tailscale debug <debug-flags | subcommand>",
		ShortHelp:  "调试命令",
		LongHelp:   hidden + `"tailscale debug" 包含一些调试工具；它不是一个稳定的接口。`,
		FlagSet: (func() *flag.FlagSet {
			fs := newFlagSet("debug")
			fs.StringVar(&debugArgs.file, "file", "", "get、delete:NAME 或 NAME")
			fs.StringVar(&debugArgs.cpuFile, "cpu-profile", "", "若非空，则抓取 CPU 性能分析文件 --profile-seconds 秒并写入此文件；- 表示写入 stdout")
			fs.StringVar(&debugArgs.memFile, "mem-profile", "", "若非空，则抓取内存性能分析文件并写入此文件；- 表示写入 stdout")
			fs.IntVar(&debugArgs.cpuSec, "profile-seconds", 15, "当 --cpu-profile 非空时，运行 CPU 性能分析的秒数")
			return fs
		})(),
		Subcommands: nonNilCmds([]*ffcli.Command{
			{
				Name:       "derp-map",
				ShortUsage: "tailscale debug derp-map",
				Exec:       runDERPMap,
				ShortHelp:  "打印 DERP 映射",
			},
			{
				Name:       "component-logs",
				ShortUsage: "tailscale debug component-logs [" + strings.Join(ipn.DebuggableComponents, "|") + "]",
				Exec:       runDebugComponentLogs,
				ShortHelp:  "启用/禁用某个组件的调试日志",
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("component-logs")
					fs.DurationVar(&debugComponentLogsArgs.forDur, "for", time.Hour, "启用调试日志的时长；零或负数表示禁用")
					return fs
				})(),
			},
			{
				Name:       "daemon-goroutines",
				ShortUsage: "tailscale debug daemon-goroutines",
				Exec:       runDaemonGoroutines,
				ShortHelp:  "打印 tailscaled 的 goroutine",
			},
			{
				Name:       "daemon-logs",
				ShortUsage: "tailscale debug daemon-logs",
				Exec:       runDaemonLogs,
				ShortHelp:  "查看 tailscaled 的服务端日志",
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("daemon-logs")
					fs.IntVar(&daemonLogsArgs.verbose, "verbose", 0, "详细级别")
					fs.BoolVar(&daemonLogsArgs.time, "time", false, "包含客户端时间")
					return fs
				})(),
			},
			{
				Name:       "daemon-bus-events",
				ShortUsage: "tailscale debug daemon-bus-events",
				Exec:       runDaemonBusEvents,
				ShortHelp:  "查看 tailscaled 总线上的事件",
			},
			{
				Name:       "daemon-bus-graph",
				ShortUsage: "tailscale debug daemon-bus-graph",
				Exec:       runDaemonBusGraph,
				ShortHelp:  "打印 tailscaled 总线的图",
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("debug-bus-graph")
					fs.StringVar(&daemonBusGraphArgs.format, "format", "json", "输出格式 [json/dot]")
					return fs
				})(),
			},
			{
				Name:       "daemon-bus-queues",
				ShortUsage: "tailscale debug daemon-bus-queues",
				Exec:       runDaemonBusQueues,
				ShortHelp:  "打印每个客户端的事件总线队列深度",
			},
			{
				Name:       "metrics",
				ShortUsage: "tailscale debug metrics",
				Exec:       runDaemonMetrics,
				ShortHelp:  "打印 tailscaled 的指标",
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("metrics")
					fs.BoolVar(&metricsArgs.watch, "watch", false, "打印增量值的 JSON 转储")
					return fs
				})(),
			},
			{
				Name:       "env",
				ShortUsage: "tailscale debug env",
				Exec:       runEnv,
				ShortHelp:  "打印 cmd/tailscale 的环境",
			},
			{
				Name:       "stat",
				ShortUsage: "tailscale debug stat <files...>",
				Exec:       runStat,
				ShortHelp:  "查看文件状态",
			},
			{
				Name:       "hostinfo",
				ShortUsage: "tailscale debug hostinfo",
				Exec:       runHostinfo,
				ShortHelp:  "打印 hostinfo",
			},
			{
				Name:       "local-creds",
				ShortUsage: "tailscale debug local-creds",
				Exec:       runLocalCreds,
				ShortHelp:  "打印如何访问 Tailscale LocalAPI",
			},
			{
				Name:       "localapi",
				ShortUsage: "tailscale debug localapi [<method>] <path> [<body| \"-\">]",
				Exec:       runLocalAPI,
				ShortHelp:  "直接调用一个 LocalAPI 方法",
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("localapi")
					fs.BoolVar(&localAPIFlags.verbose, "v", false, "详细模式；转储 HTTP 头")
					return fs
				})(),
			},
			{
				Name:       "restun",
				ShortUsage: "tailscale debug restun",
				Exec:       localAPIAction("restun"),
				ShortHelp:  "强制进行一次 magicsock restun",
			},
			{
				Name:       "rebind",
				ShortUsage: "tailscale debug rebind",
				Exec:       localAPIAction("rebind"),
				ShortHelp:  "强制进行一次 magicsock rebind",
			},
			{
				Name:       "rotate-disco-key",
				ShortUsage: "tailscale debug rotate-disco-key",
				Exec:       localAPIAction("rotate-disco-key"),
				ShortHelp:  "轮换发现密钥",
			},
			{
				Name:       "derp-set-on-demand",
				ShortUsage: "tailscale debug derp-set-on-demand",
				Exec:       localAPIAction("derp-set-homeless"),
				ShortHelp:  "启用 DERP 按需模式（会破坏可达性）",
			},
			{
				Name:       "derp-unset-on-demand",
				ShortUsage: "tailscale debug derp-unset-on-demand",
				Exec:       localAPIAction("derp-unset-homeless"),
				ShortHelp:  "禁用 DERP 按需模式",
			},
			{
				Name:       "break-tcp-conns",
				ShortUsage: "tailscale debug break-tcp-conns",
				Exec:       localAPIAction("break-tcp-conns"),
				ShortHelp:  "断开 daemon 的任何已打开 TCP 连接",
			},
			{
				Name:       "break-derp-conns",
				ShortUsage: "tailscale debug break-derp-conns",
				Exec:       localAPIAction("break-derp-conns"),
				ShortHelp:  "断开 daemon 的任何已打开 DERP 连接",
			},
			{
				Name:       "pick-new-derp",
				ShortUsage: "tailscale debug pick-new-derp",
				Exec:       localAPIAction("pick-new-derp"),
				ShortHelp:  "在短期内切换到另一个随机的 DERP 归属区域",
			},
			{
				Name:       "force-prefer-derp",
				ShortUsage: "tailscale debug force-prefer-derp",
				Exec:       forcePreferDERP,
				ShortHelp:  "若可达则优先使用给定区域 ID（直到重启，或 0 表示清除）",
			},
			{
				Name:       "force-netmap-update",
				ShortUsage: "tailscale debug force-netmap-update",
				Exec:       localAPIAction("force-netmap-update"),
				ShortHelp:  "强制进行一次完整的空操作 netmap 更新（用于负载测试）",
			},
			{
				// TODO(bradfitz,maisem): eventually promote this out of debug
				Name:       "reload-config",
				ShortUsage: "tailscale debug reload-config",
				Exec:       reloadConfig,
				ShortHelp:  "重新加载配置",
			},
			{
				Name:       "control-knobs",
				ShortUsage: "tailscale debug control-knobs",
				Exec:       debugControlKnobs,
				ShortHelp:  "查看当前控制旋钮",
			},
			{
				Name:       "prefs",
				ShortUsage: "tailscale debug prefs",
				Exec:       runPrefs,
				ShortHelp:  "打印首选项",
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("prefs")
					fs.BoolVar(&prefsArgs.pretty, "pretty", false, "若为 true，则美化输出")
					return fs
				})(),
			},
			{
				Name:       "watch-ipn",
				ShortUsage: "tailscale debug watch-ipn",
				Exec:       runWatchIPN,
				ShortHelp:  "订阅 IPN 消息总线",
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("watch-ipn")
					fs.BoolVar(&watchIPNArgs.initial, "initial", false, "在首条消息中包含初始后端状态和首选项")
					fs.IntVar(&watchIPNArgs.count, "count", 0, "打印这么多条状态后退出，或 0 表示一直运行")
					fs.BoolVar(&watchIPNArgs.engineUpdates, "engine-updates", false, "设置 NotifyWatchEngineUpdates：发送 Engine 更新")
					fs.BoolVar(&watchIPNArgs.initialDriveShares, "initial-drive-shares", false, "设置 NotifyInitialDriveShares：在首条消息中发送当前 Taildrive 共享")
					fs.BoolVar(&watchIPNArgs.initialOutgoingFiles, "initial-outgoing-files", false, "设置 NotifyInitialOutgoingFiles：在首条消息中发送当前 Taildrop 传出文件")
					fs.BoolVar(&watchIPNArgs.initialHealthState, "initial-health", false, "设置 NotifyInitialHealthState：在首条消息中发送当前 health.State")
					fs.BoolVar(&watchIPNArgs.healthActions, "health-actions", false, "设置 NotifyHealthActions：在 health.State 中包含所有 PrimaryActions")
					fs.BoolVar(&watchIPNArgs.initialSuggestedExitNode, "initial-suggested-exit-node", false, "设置 NotifyInitialSuggestedExitNode：在首条消息中发送当前 SuggestedExitNode")
					fs.BoolVar(&watchIPNArgs.initialClientVersion, "initial-client-version", false, "设置 NotifyInitialClientVersion：在首条消息中发送当前 ClientVersion")
					fs.BoolVar(&watchIPNArgs.peerChanges, "peer-changes", true, "设置 NotifyPeerChanges：发送 PeersChanged 和 PeersRemoved 更新")
					fs.BoolVar(&watchIPNArgs.initialStatus, "initial-status", false, "设置 NotifyInitialStatus：在首条消息中发送当前 ipnstate.Status")
					fs.BoolVar(&watchIPNArgs.peerPatches, "peer-patches", true, "设置 NotifyPeerPatches：发送按字段细分的 peer 补丁")
					fs.BoolVar(&watchIPNArgs.peerWireGuardState, "peer-wireguard-state", false, "设置 NotifyPeerWireGuardState：发送 WireGuard 会话状态通知")
					return fs
				})(),
			},
			{
				Name:       "netmap",
				ShortUsage: "tailscale debug netmap",
				Exec:       runNetmap,
				ShortHelp:  "打印当前网络映射",
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("netmap")
					return fs
				})(),
			},
			{
				Name: "via",
				ShortUsage: "tailscale debug via <site-id> <v4-cidr>\n" +
					"tailscale debug via <v6-route>",
				Exec:      runVia,
				ShortHelp: "在站点专属 IPv4 CIDR 与 IPv6 'via' 路由之间转换",
			},
			{
				Name:       "ts2021",
				ShortUsage: "tailscale debug ts2021",
				Exec:       runTS2021,
				ShortHelp:  "调试 ts2021 协议连通性",
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("ts2021")
					fs.StringVar(&ts2021Args.host, "host", "controlplane.tailscale.com", "控制平面的主机名")
					fs.IntVar(&ts2021Args.version, "version", int(tailcfg.CurrentCapabilityVersion), "协议版本")
					fs.BoolVar(&ts2021Args.verbose, "verbose", false, "输出更详细的信息")
					fs.StringVar(&ts2021Args.aceHost, "ace", "", "若非空，则使用此 ACE 服务器 IP/主机名作为候选路径")
					fs.StringVar(&ts2021Args.dialPlanJSONFile, "dial-plan", "", "若非空，则使用此 JSON 文件配置拨号计划")
					return fs
				})(),
			},
			{
				Name:       "set-expire",
				ShortUsage: "tailscale debug set-expire --in=1m",
				Exec:       runSetExpire,
				ShortHelp:  "为测试操作节点密钥过期时间",
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("set-expire")
					fs.DurationVar(&setExpireArgs.in, "in", 0, "若非零，则将节点密钥设为从现在起此段时间后过期")
					return fs
				})(),
			},
			{
				Name:       "dev-store-set",
				ShortUsage: "tailscale debug dev-store-set",
				Exec:       runDevStoreSet,
				ShortHelp:  "在开发过程中设置键值对",
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("store-set")
					fs.BoolVar(&devStoreSetArgs.danger, "danger", false, "确认危险操作")
					return fs
				})(),
			},
			{
				Name:       "derp",
				ShortUsage: "tailscale debug derp",
				Exec:       runDebugDERP,
				ShortHelp:  "测试 DERP 配置",
			},
			ccall(debugCaptureCmd),
			ccall(debugPortmapCmd),
			{
				Name:       "peer-endpoint-changes",
				ShortUsage: "tailscale debug peer-endpoint-changes <hostname-or-IP>",
				Exec:       runPeerEndpointChanges,
				ShortHelp:  "打印关于节点端点变更的调试信息",
			},
			{
				Name:       "dial-types",
				ShortUsage: "tailscale debug dial-types <hostname-or-IP> <port>",
				Exec:       runDebugDialTypes,
				ShortHelp:  "打印关于连接到给定主机或 IP 的调试信息",
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("dial-types")
					fs.StringVar(&debugDialTypesArgs.network, "network", "tcp", `要拨号的网络类型（"tcp"、"udp" 等）`)
					return fs
				})(),
			},
			{
				Name:       "resolve",
				ShortUsage: "tailscale debug resolve <hostname>",
				Exec:       runDebugResolve,
				ShortHelp:  "执行 DNS 查找",
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("resolve")
					fs.StringVar(&resolveArgs.net, "net", "ip", "要解析的网络类型（ip、ip4、ip6）")
					return fs
				})(),
			},
			{
				Name:       "go-buildinfo",
				ShortUsage: "tailscale debug go-buildinfo",
				ShortHelp:  "打印 Go 的 runtime/debug.BuildInfo",
				Exec:       runGoBuildInfo,
			},
			{
				Name:       "peer-relay-servers",
				ShortUsage: "tailscale debug peer-relay-servers",
				ShortHelp:  "打印当前候选节点中继服务器集合",
				Exec:       runPeerRelayServers,
			},
			{
				Name:       "test-risk",
				ShortUsage: "tailscale debug test-risk",
				ShortHelp:  "执行一次模拟的危险操作",
				Exec:       runTestRisk,
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("test-risk")
					fs.StringVar(&testRiskArgs.acceptedRisk, "accept-risk", "", "以逗号分隔的已接受风险列表")
					return fs
				})(),
			},
			{
				Name:       "statedir",
				ShortUsage: "tailscale debug statedir",
				ShortHelp:  "打印状态目录的位置（若存在）",
				Exec:       runPrintStateDir,
			},
			ccall(debugPeerRelayCmd),
			ccall(debugClearNetmapCacheCmd),
		}...),
	}
}

func runGoBuildInfo(ctx context.Context, args []string) error {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return errors.New("无 Go 构建信息")
	}
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "\t")
	return e.Encode(bi)
}

var debugArgs struct {
	file    string
	cpuSec  int
	cpuFile string
	memFile string
}

func writeProfile(dst string, v []byte) error {
	if dst == "-" {
		_, err := Stdout.Write(v)
		return err
	}
	return os.WriteFile(dst, v, 0600)
}

func outName(dst string) string {
	if dst == "-" {
		return "stdout"
	}
	if runtime.GOOS == "darwin" {
		return fmt.Sprintf("%s（警告：沙箱化的 macOS 二进制文件会写入 Library/Containers；请使用 - 写入 stdout 再重定向到文件）", dst)
	}
	return dst
}

func runDebug(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("tailscale debug: 未知子命令：%s", args[0])
	}
	var usedFlag bool
	if out := debugArgs.cpuFile; out != "" {
		usedFlag = true // TODO(bradfitz): add "pprof" subcommand
		log.Printf("正在抓取 CPU 性能分析文件，时长 %v 秒 ...", debugArgs.cpuSec)
		if v, err := localClient.Pprof(ctx, "profile", debugArgs.cpuSec); err != nil {
			return err
		} else {
			if err := writeProfile(out, v); err != nil {
				return err
			}
			log.Printf("CPU 性能分析文件已写入 %s", outName(out))
		}
	}
	if out := debugArgs.memFile; out != "" {
		usedFlag = true // TODO(bradfitz): add "pprof" subcommand
		log.Printf("正在抓取内存性能分析文件 ...")
		if v, err := localClient.Pprof(ctx, "heap", 0); err != nil {
			return err
		} else {
			if err := writeProfile(out, v); err != nil {
				return err
			}
			log.Printf("内存性能分析文件已写入 %s", outName(out))
		}
	}
	if debugArgs.file != "" {
		usedFlag = true // TODO(bradfitz): add "file" subcommand
		if debugArgs.file == "get" {
			wfs, err := localClient.WaitingFiles(ctx)
			if err != nil {
				fatalf("%v\n", err)
			}
			e := json.NewEncoder(Stdout)
			e.SetIndent("", "\t")
			e.Encode(wfs)
			return nil
		}
		if name, ok := strings.CutPrefix(debugArgs.file, "delete:"); ok {
			return localClient.DeleteWaitingFile(ctx, name)
		}
		rc, size, err := localClient.GetWaitingFile(ctx, debugArgs.file)
		if err != nil {
			return err
		}
		log.Printf("大小：%v\n", size)
		io.Copy(Stdout, rc)
		return nil
	}
	if usedFlag {
		// TODO(bradfitz): delete this path when all debug flags are migrated
		// to subcommands.
		return nil
	}
	return errors.New("tailscale debug: 需要子命令或标志")
}

func runLocalCreds(ctx context.Context, args []string) error {
	port, token, err := safesocket.LocalTCPPortAndToken()
	if err == nil {
		printf("curl -u:%s http://localhost:%d/localapi/v0/status\n", token, port)
		return nil
	}
	if runtime.GOOS == "windows" {
		runLocalAPIProxy()
		return nil
	}
	printf("curl --unix-socket %s http://local-tailscaled.sock/localapi/v0/status\n", paths.DefaultTailscaledSocket())
	return nil
}

func looksLikeHTTPMethod(s string) bool {
	if len(s) > len("OPTIONS") {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

var localAPIFlags struct {
	verbose bool
}

func runLocalAPI(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("至少需要一个参数")
	}
	method := "GET"
	if looksLikeHTTPMethod(args[0]) {
		method = args[0]
		args = args[1:]
		if len(args) == 0 {
			return errors.New("方法后至少需要一个参数")
		}
	}
	path := args[0]
	if !strings.HasPrefix(path, "/localapi/") {
		if !strings.Contains(path, "/") {
			path = "/localapi/v0/" + path
		} else {
			path = "/localapi/" + path
		}
	}

	var body io.Reader
	if len(args) > 1 {
		if args[1] == "-" {
			fmt.Fprintf(Stderr, "# 正在从 stdin 读取请求体...\n")
			all, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("读取 Stdin：%q", err)
			}
			body = bytes.NewReader(all)
		} else {
			body = strings.NewReader(args[1])
		}
	}
	req, err := http.NewRequest(method, "http://local-tailscaled.sock"+path, body)
	if err != nil {
		return err
	}
	fmt.Fprintf(Stderr, "# 正在发起请求 %s %s\n", method, path)

	res, err := localClient.DoLocalRequest(req)
	if err != nil {
		return err
	}
	is2xx := res.StatusCode >= 200 && res.StatusCode <= 299
	if localAPIFlags.verbose {
		res.Write(Stdout)
	} else {
		if !is2xx {
			fmt.Fprintf(Stderr, "# 响应状态 %s\n", res.Status)
		}
		io.Copy(Stdout, res.Body)
	}
	if is2xx {
		return nil
	}
	return errors.New(res.Status)
}

type localClientRoundTripper struct{}

func (localClientRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return localClient.DoLocalRequest(req)
}

func runLocalAPIProxy() {
	rp := httputil.NewSingleHostReverseProxy(&url.URL{
		Scheme: "http",
		Host:   apitype.LocalAPIHost,
		Path:   "/",
	})
	dir := rp.Director
	rp.Director = func(req *http.Request) {
		dir(req)
		req.Host = ""
		req.RequestURI = ""
	}
	rp.Transport = localClientRoundTripper{}
	lc, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("正在 http://%s 提供 LocalAPI 代理\n", lc.Addr())
	fmt.Printf("curl.exe http://%v/localapi/v0/status\n", lc.Addr())
	fmt.Printf("按 Ctrl+C 停止")
	http.Serve(lc, rp)
}

var prefsArgs struct {
	pretty bool
}

func runPrefs(ctx context.Context, args []string) error {
	prefs, err := localClient.GetPrefs(ctx)
	if err != nil {
		return err
	}
	if prefsArgs.pretty {
		outln(prefs.Pretty())
	} else {
		j, _ := json.MarshalIndent(prefs, "", "\t")
		outln(string(j))
	}
	return nil
}

var watchIPNArgs struct {
	initial bool
	count   int

	engineUpdates            bool
	initialDriveShares       bool
	initialOutgoingFiles     bool
	initialHealthState       bool
	healthActions            bool
	initialSuggestedExitNode bool
	initialClientVersion     bool
	peerChanges              bool
	initialStatus            bool
	peerPatches              bool
	peerWireGuardState       bool
}

func runWatchIPN(ctx context.Context, args []string) error {
	mask := ipn.NotifyNoNetMap
	if watchIPNArgs.initial {
		mask |= ipn.NotifyInitialState | ipn.NotifyInitialPrefs
	}
	if watchIPNArgs.engineUpdates {
		mask |= ipn.NotifyWatchEngineUpdates
	}
	if watchIPNArgs.initialDriveShares {
		mask |= ipn.NotifyInitialDriveShares
	}
	if watchIPNArgs.initialOutgoingFiles {
		mask |= ipn.NotifyInitialOutgoingFiles
	}
	if watchIPNArgs.initialHealthState {
		mask |= ipn.NotifyInitialHealthState
	}
	if watchIPNArgs.healthActions {
		mask |= ipn.NotifyHealthActions
	}
	if watchIPNArgs.initialSuggestedExitNode {
		mask |= ipn.NotifyInitialSuggestedExitNode
	}
	if watchIPNArgs.initialClientVersion {
		mask |= ipn.NotifyInitialClientVersion
	}
	if watchIPNArgs.peerChanges {
		mask |= ipn.NotifyPeerChanges
	}
	if watchIPNArgs.initialStatus {
		mask |= ipn.NotifyInitialStatus
	}
	if watchIPNArgs.peerPatches {
		mask |= ipn.NotifyPeerPatches
	}
	if watchIPNArgs.peerWireGuardState {
		mask |= ipn.NotifyPeerWireGuardState
	}
	watcher, err := localClient.WatchIPNBus(ctx, mask)
	if err != nil {
		return err
	}
	defer watcher.Close()
	fmt.Fprintf(Stderr, "已连接。\n")
	for seen := 0; watchIPNArgs.count == 0 || seen < watchIPNArgs.count; seen++ {
		n, err := watcher.Next()
		if err != nil {
			return err
		}
		j, _ := json.MarshalIndent(n, "", "\t")
		fmt.Printf("%s\n", j)
	}
	return nil
}

func runNetmap(ctx context.Context, args []string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	raw, err := localClient.DebugResultJSON(ctx, "current-netmap")
	if err != nil {
		return err
	}
	j, _ := json.MarshalIndent(raw, "", "\t")
	fmt.Printf("%s\n", j)
	return nil
}

func runDERPMap(ctx context.Context, args []string) error {
	dm, err := localClient.CurrentDERPMap(ctx)
	if err != nil {
		return fmt.Errorf(
			"获取本地 derp 映射失败，请改用 `curl %s/derpmap/default`：%w", ipn.DefaultControlURL, err,
		)
	}
	enc := json.NewEncoder(Stdout)
	enc.SetIndent("", "\t")
	enc.Encode(dm)
	return nil
}

func forcePreferDERP(ctx context.Context, args []string) error {
	var n int
	if len(args) != 1 {
		return errors.New("需要且仅需要一个整数参数")
	}
	n, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("需要且仅需要一个整数参数：%w", err)
	}
	b, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("序列化 DERP 区域失败：%w", err)
	}
	if err := localClient.DebugActionBody(ctx, "force-prefer-derp", bytes.NewReader(b)); err != nil {
		return fmt.Errorf("强制首选 DERP 失败：%w", err)
	}
	return nil
}

func localAPIAction(action string) func(context.Context, []string) error {
	return func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return errors.New("意外的参数")
		}
		return localClient.DebugAction(ctx, action)
	}
}

func reloadConfig(ctx context.Context, args []string) error {
	ok, err := localClient.ReloadConfig(ctx)
	if err != nil {
		return err
	}
	if ok {
		printf("配置已重新加载\n")
		return nil
	}
	printf("未使用配置模式\n")
	os.Exit(1)
	panic("unreachable")
}

func runEnv(ctx context.Context, args []string) error {
	for _, e := range os.Environ() {
		outln(e)
	}
	return nil
}

func runStat(ctx context.Context, args []string) error {
	for _, a := range args {
		fi, err := os.Lstat(a)
		if err != nil {
			printf("%s: %v\n", a, err)
			continue
		}
		printf("%s: %v, %v\n", a, fi.Mode(), fi.Size())
		if fi.IsDir() {
			ents, _ := os.ReadDir(a)
			for i, ent := range ents {
				if i == 25 {
					printf("  ...\n")
					break
				}
				printf("  - %s\n", ent.Name())
			}
		}
	}
	return nil
}

func runHostinfo(ctx context.Context, args []string) error {
	hi := hostinfo.New()
	j, _ := json.MarshalIndent(hi, "", "  ")
	Stdout.Write(j)
	return nil
}

func runDaemonGoroutines(ctx context.Context, args []string) error {
	goroutines, err := localClient.Goroutines(ctx)
	if err != nil {
		return err
	}
	Stdout.Write(goroutines)
	return nil
}

var daemonLogsArgs struct {
	verbose int
	time    bool
}

func runDaemonLogs(ctx context.Context, args []string) error {
	logs, err := localClient.TailDaemonLogs(ctx)
	if err != nil {
		return err
	}
	d := json.NewDecoder(logs)
	for {
		type logtail struct {
			Time string `json:"client_time"`
		}
		var line struct {
			Text    string  `json:"text"`
			Verbose int     `json:"v"`
			Logtail logtail `json:"logtail"`
		}
		err := d.Decode(&line)
		if err != nil {
			return err
		}
		line.Text = strings.TrimSpace(line.Text)
		if line.Text == "" || line.Verbose > daemonLogsArgs.verbose {
			continue
		}
		if daemonLogsArgs.time && line.Logtail.Time != "" {
			fmt.Printf("%s %s\n", line.Logtail.Time, line.Text)
		} else {
			fmt.Println(line.Text)
		}
	}
}

func runDaemonBusEvents(ctx context.Context, args []string) error {
	for line, err := range localClient.StreamBusEvents(ctx) {
		if err != nil {
			return err
		}
		fmt.Printf("[%d][%q][from: %q][to: %q] %s\n", line.Count, line.Type,
			line.From, line.To, line.Event)
	}
	return nil
}

var daemonBusGraphArgs struct {
	format string
}

func runDaemonBusGraph(ctx context.Context, args []string) error {
	graph, err := localClient.EventBusGraph(ctx)
	if err != nil {
		return err
	}
	if format := daemonBusGraphArgs.format; format != "json" && format != "dot" {
		return fmt.Errorf("无法识别的输出格式 %q", format)
	}
	if daemonBusGraphArgs.format == "dot" {
		var topics eventbus.DebugTopics
		if err := json.Unmarshal(graph, &topics); err != nil {
			return fmt.Errorf("无法解析 json：%w", err)
		}
		fmt.Print(generateDOTGraph(topics.Topics))
	} else {
		fmt.Print(string(graph))
	}
	return nil
}

func runDaemonBusQueues(ctx context.Context, args []string) error {
	data, err := localClient.EventBusQueues(ctx)
	if err != nil {
		return err
	}
	fmt.Print(string(data))
	return nil
}

// generateDOTGraph generates the DOT graph format based on the events
func generateDOTGraph(topics []eventbus.DebugTopic) string {
	var sb strings.Builder
	sb.WriteString("digraph event_bus {\n")

	for _, topic := range topics {
		// If no subscribers, still ensure the topic is drawn
		if len(topic.Subscribers) == 0 {
			topic.Subscribers = append(topic.Subscribers, "no-subscribers")
		}
		for _, subscriber := range topic.Subscribers {
			fmt.Fprintf(&sb, "\t%q -> %q [label=%q];\n",
				topic.Publisher, subscriber, cmp.Or(topic.Name, "???"))
		}
	}

	sb.WriteString("}\n")
	return sb.String()
}

var metricsArgs struct {
	watch bool
}

func runDaemonMetrics(ctx context.Context, args []string) error {
	last := map[string]int64{}
	for {
		out, err := localClient.DaemonMetrics(ctx)
		if err != nil {
			return err
		}
		if !metricsArgs.watch {
			Stdout.Write(out)
			return nil
		}
		bs := bufio.NewScanner(bytes.NewReader(out))
		type change struct {
			name     string
			from, to int64
		}
		var changes []change
		var maxNameLen int
		for bs.Scan() {
			line := bytes.TrimSpace(bs.Bytes())
			if len(line) == 0 || line[0] == '#' {
				continue
			}
			f := strings.Fields(string(line))
			if len(f) != 2 {
				continue
			}
			name := f[0]
			n, _ := strconv.ParseInt(f[1], 10, 64)
			prev, ok := last[name]
			if ok && prev == n {
				continue
			}
			last[name] = n
			if !ok {
				continue
			}
			changes = append(changes, change{name, prev, n})
			if len(name) > maxNameLen {
				maxNameLen = len(name)
			}
		}
		if len(changes) > 0 {
			format := fmt.Sprintf("%%-%ds %%+5d => %%v\n", maxNameLen)
			for _, c := range changes {
				fmt.Fprintf(Stdout, format, c.name, c.to-c.from, c.to)
			}
			io.WriteString(Stdout, "\n")
		}
		time.Sleep(time.Second)
	}
}

func runVia(ctx context.Context, args []string) error {
	switch len(args) {
	default:
		return errors.New("需要 <site-id> <v4-cidr> 或 <v6-route> 之一")
	case 1:
		ipp, err := netip.ParsePrefix(args[0])
		if err != nil {
			return err
		}
		if !ipp.Addr().Is6() {
			return errors.New("当只给出一个参数时，应为一个 IPv6 CIDR")
		}
		if !tsaddr.TailscaleViaRange().Contains(ipp.Addr()) {
			return errors.New("不是 via 路由")
		}
		if ipp.Bits() < 96 {
			return errors.New("长度过短，需要 /96 或更长")
		}
		v4 := tsaddr.UnmapVia(ipp.Addr())
		a := ipp.Addr().As16()
		siteID := binary.BigEndian.Uint32(a[8:12])
		printf("site %v (0x%x), %v\n", siteID, siteID, netip.PrefixFrom(v4, ipp.Bits()-96))
	case 2:
		siteID, err := strconv.ParseUint(args[0], 0, 32)
		if err != nil {
			return fmt.Errorf("无效的 site-id %q；必须为十进制或以 0x 开头的十六进制", args[0])
		}
		if siteID > 0xffff {
			return fmt.Errorf("大于 65535 的 site-id 值目前保留")
		}
		ipp, err := netip.ParsePrefix(args[1])
		if err != nil {
			return err
		}
		via, err := tsaddr.MapVia(uint32(siteID), ipp)
		if err != nil {
			return err
		}
		outln(via)
	}
	return nil
}

var ts2021Args struct {
	host    string // "controlplane.tailscale.com"
	version int    // 27 or whatever
	verbose bool
	aceHost string // if non-empty, FQDN of https ACE server to use ("ace.example.com")

	dialPlanJSONFile string // if non-empty, path to JSON file [tailcfg.ControlDialPlan] JSON
}

func runTS2021(ctx context.Context, args []string) error {
	log.SetOutput(Stdout)
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	keysURL := "https://" + ts2021Args.host + "/key?v=" + strconv.Itoa(ts2021Args.version)

	keyTransport := netutil.NewDefaultTransport()
	if ts2021Args.aceHost != "" {
		log.Printf("使用 ACE 服务器 %q", ts2021Args.aceHost)
		keyTransport.Proxy = nil
		keyTransport.DialContext = (&ace.Dialer{ACEHost: ts2021Args.aceHost}).Dial
	}

	if ts2021Args.verbose {
		u, err := url.Parse(keysURL)
		if err != nil {
			return err
		}
		if proxyFromEnv, ok := feature.HookProxyFromEnvironment.GetOk(); ok {
			proxy, err := proxyFromEnv(&http.Request{URL: u})
			log.Printf("tshttpproxy.ProxyFromEnvironment = (%v, %v)", proxy, err)
		}
	}
	machinePrivate := key.NewMachine()
	var dialer net.Dialer

	var keys struct {
		PublicKey key.MachinePublic
	}
	log.Printf("正在从 %s 获取密钥 ...", keysURL)
	req, err := http.NewRequestWithContext(ctx, "GET", keysURL, nil)
	if err != nil {
		return err
	}
	res, err := keyTransport.RoundTrip(req)
	if err != nil {
		log.Printf("Do：%v", err)
		return err
	}
	if res.StatusCode != 200 {
		log.Printf("状态：%v", res.Status)
		return errors.New(res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(&keys); err != nil {
		log.Printf("JSON：%v", err)
		return fmt.Errorf("解码 /keys JSON 失败：%w", err)
	}
	res.Body.Close()
	if ts2021Args.verbose {
		log.Printf("获取到公钥：%v", keys.PublicKey)
	}

	dialFunc := func(ctx context.Context, network, address string) (net.Conn, error) {
		log.Printf("Dial(%q, %q) ...", network, address)
		c, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			// skip logging context cancellation errors
			if !errors.Is(err, context.Canceled) {
				log.Printf("Dial(%q, %q) = %v", network, address, err)
			}
		} else {
			log.Printf("Dial(%q, %q) = %v / %v", network, address, c.LocalAddr(), c.RemoteAddr())
		}
		return c, err
	}
	var logf logger.Logf
	if ts2021Args.verbose {
		logf = log.Printf
	}

	bus := eventbus.New()
	defer bus.Close()

	netMon, err := netmon.New(bus, logger.WithPrefix(logf, "netmon: "))
	if err != nil {
		return fmt.Errorf("创建 netmon：%w", err)
	}

	var dialPlan *tailcfg.ControlDialPlan
	if ts2021Args.dialPlanJSONFile != "" {
		b, err := os.ReadFile(ts2021Args.dialPlanJSONFile)
		if err != nil {
			return fmt.Errorf("读取拨号计划 JSON 文件：%w", err)
		}
		dialPlan = new(tailcfg.ControlDialPlan)
		if err := json.Unmarshal(b, dialPlan); err != nil {
			return fmt.Errorf("反序列化拨号计划 JSON 文件：%w", err)
		}
	} else if ts2021Args.aceHost != "" {
		dialPlan = &tailcfg.ControlDialPlan{
			Candidates: []tailcfg.ControlIPCandidate{
				{
					ACEHost:        ts2021Args.aceHost,
					DialTimeoutSec: 10,
				},
			},
		}
	}

	opts := ts2021.ClientOpts{
		ServerURL: "https://" + ts2021Args.host,
		DialPlan: func() *tailcfg.ControlDialPlan {
			return dialPlan
		},
		Logf:          logf,
		NetMon:        netMon,
		PrivKey:       machinePrivate,
		ServerPubKey:  keys.PublicKey,
		Dialer:        tsdial.NewFromFuncForDebug(logf, dialFunc),
		DNSCache:      &dnscache.Resolver{},
		HealthTracker: &health.Tracker{},
	}

	// TODO: 	ProtocolVersion: uint16(ts2021Args.version),
	const tries = 2
	for i := range tries {
		err := tryConnect(ctx, keys.PublicKey, opts)
		if err != nil {
			log.Printf("error on attempt %d/%d: %v", i+1, tries, err)
			continue
		}
		break
	}
	return nil
}

func tryConnect(ctx context.Context, controlPublic key.MachinePublic, opts ts2021.ClientOpts) error {

	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(ci httptrace.GotConnInfo) {
			log.Printf("GotConn: %T", ci.Conn)
			ncc, ok := ci.Conn.(*ts2021.Conn)
			if !ok {
				return
			}
			log.Printf("did noise handshake")
			log.Printf("final underlying conn: %v / %v", ncc.LocalAddr(), ncc.RemoteAddr())
			gotPeer := ncc.Peer()
			if gotPeer != controlPublic {
				log.Fatalf("peer = %v, want %v", gotPeer, controlPublic)
			}
		},
	})

	nc, err := ts2021.NewClient(opts)
	if err != nil {
		return fmt.Errorf("NewNoiseClient：%w", err)
	}

	// Make a /whoami request to the server to verify that we can actually
	// communicate over the newly-established connection.
	whoamiURL := "https://" + ts2021Args.host + "/machine/whoami"
	req, err := http.NewRequestWithContext(ctx, "GET", whoamiURL, nil)
	if err != nil {
		return err
	}
	resp, err := nc.Do(req)
	if err != nil {
		return fmt.Errorf("RoundTrip whoami 请求：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("whoami request returned status %v", resp.Status)
	} else {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("读取 whoami 响应：%w", err)
		}
		log.Printf("whoami response: %q", body)
	}
	return nil
}

var debugComponentLogsArgs struct {
	forDur time.Duration
}

func runDebugComponentLogs(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("用法：tailscale debug component-logs [" + strings.Join(ipn.DebuggableComponents, "|") + "]")
	}
	component := args[0]
	dur := debugComponentLogsArgs.forDur

	err := localClient.SetComponentDebugLogging(ctx, component, dur)
	if err != nil {
		return err
	}
	if debugComponentLogsArgs.forDur <= 0 {
		fmt.Printf("已禁用组件 %q 的调试日志\n", component)
	} else {
		fmt.Printf("已启用组件 %q 的调试日志，时长 %v\n", component, dur)
	}
	return nil
}

var devStoreSetArgs struct {
	danger bool
}

func runDevStoreSet(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return errors.New("用法：tailscale debug dev-store-set --danger <key> <value>")
	}
	if !devStoreSetArgs.danger {
		return errors.New("此命令具有危险性；请使用 --danger 继续")
	}
	key, val := args[0], args[1]
	if val == "-" {
		valb, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		val = string(valb)
	}
	return localClient.SetDevStoreKeyValue(ctx, key, val)
}

func runDebugDERP(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("用法：tailscale debug derp <region>")
	}
	st, err := localClient.DebugDERPRegion(ctx, args[0])
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", must.Get(json.MarshalIndent(st, "", " ")))
	return nil
}

var setExpireArgs struct {
	in time.Duration
}

func runSetExpire(ctx context.Context, args []string) error {
	if len(args) != 0 || setExpireArgs.in == 0 {
		return errors.New("用法：tailscale debug set-expire --in=<duration>")
	}
	return localClient.DebugSetExpireIn(ctx, setExpireArgs.in)
}

func runPeerEndpointChanges(ctx context.Context, args []string) error {
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
		return errors.New("用法：tailscale debug peer-endpoint-changes <hostname-or-IP>")
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

	if ip != hostOrIP {
		log.Printf("lookup %q => %q", hostOrIP, ip)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "http://local-tailscaled.sock/localapi/v0/debug-peer-endpoint-changes?ip="+ip, nil)
	if err != nil {
		return err
	}

	resp, err := localClient.DoLocalRequest(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var dst bytes.Buffer
	if err := json.Indent(&dst, body, "", "  "); err != nil {
		return fmt.Errorf("缩进返回的 JSON：%w", err)
	}

	if ss := dst.String(); !strings.HasSuffix(ss, "\n") {
		dst.WriteByte('\n')
	}
	fmt.Printf("%s", dst.String())
	return nil
}

func debugControlKnobs(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("意外的参数")
	}
	v, err := localClient.DebugResultJSON(ctx, "control-knobs")
	if err != nil {
		return err
	}
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	e.Encode(v)
	return nil
}

var debugDialTypesArgs struct {
	network string
}

func runDebugDialTypes(ctx context.Context, args []string) error {
	st, err := localClient.Status(ctx)
	if err != nil {
		return fixTailscaledConnectError(err)
	}
	description, ok := isRunningOrStarting(st)
	if !ok {
		printf("%s\n", description)
		os.Exit(1)
	}

	if len(args) != 2 || args[0] == "" || args[1] == "" {
		return errors.New("用法：tailscale debug dial-types <hostname-or-IP> <port>")
	}

	port, err := strconv.ParseUint(args[1], 10, 16)
	if err != nil {
		return fmt.Errorf("invalid port %q: %w", args[1], err)
	}

	hostOrIP := args[0]
	ip, _, err := tailscaleIPFromArg(ctx, hostOrIP)
	if err != nil {
		return err
	}
	if ip != hostOrIP {
		log.Printf("lookup %q => %q", hostOrIP, ip)
	}

	qparams := make(url.Values)
	qparams.Set("ip", ip)
	qparams.Set("port", strconv.FormatUint(port, 10))
	qparams.Set("network", debugDialTypesArgs.network)

	req, err := http.NewRequestWithContext(ctx, "POST", "http://local-tailscaled.sock/localapi/v0/debug-dial-types?"+qparams.Encode(), nil)
	if err != nil {
		return err
	}

	resp, err := localClient.DoLocalRequest(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	fmt.Printf("%s", body)
	return nil
}

var resolveArgs struct {
	net string // "ip", "ip4", "ip6""
}

func runDebugResolve(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("用法：tailscale debug resolve <hostname>")
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	host := args[0]
	ips, err := net.DefaultResolver.LookupIP(ctx, resolveArgs.net, host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		fmt.Printf("%s\n", ip)
	}
	return nil
}

func runPeerRelayServers(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("意外的参数")
	}
	v, err := localClient.DebugResultJSON(ctx, "peer-relay-servers")
	if err != nil {
		return err
	}
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	e.Encode(v)
	return nil
}

var testRiskArgs struct {
	acceptedRisk string
}

func runTestRisk(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("意外的参数")
	}
	if err := presentRiskToUser("test-risk", "这是一个测试性的危险操作。", testRiskArgs.acceptedRisk); err != nil {
		return err
	}
	fmt.Println("已执行模拟危险操作")
	return nil
}

func runPrintStateDir(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("意外的参数")
	}
	v, err := localClient.DebugResultJSON(ctx, "statedir")
	if err != nil {
		return err
	}
	statedir, ok := v.(string)
	if ok && statedir != "" {
		fmt.Println(statedir)
		return nil
	} else if ok && statedir == "" {
		return errors.New("未设置 statedir")
	} else {
		return fmt.Errorf("从调试 API 收到意外响应：%v", v)
	}
}
