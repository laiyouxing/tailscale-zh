// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/cmd/tailscale/cli/ffcomplete"
	"tailscale.com/feature/buildfeatures"
	"tailscale.com/ipn"
	"tailscale.com/net/netutil"
	"tailscale.com/net/tsaddr"
	"tailscale.com/safesocket"
	"tailscale.com/tsconst"
	"tailscale.com/types/opt"
	"tailscale.com/types/views"
	"tailscale.com/util/set"
	"tailscale.com/version"
)

var setCmd = &ffcli.Command{
	Name:       "set",
	ShortUsage: "tailscale set [flags]",
	ShortHelp:  "更改指定的偏好设置",
	LongHelp: `"tailscale set" 允许更改特定的偏好设置。

与 "tailscale up" 不同，此命令不要求提供完整的期望设置集。

只有显式指定的设置会被修改。没有默认值。`,
	FlagSet:   setFlagSet,
	Exec:      runSet,
	UsageFunc: usageFuncNoDefaultValues,
}

type setArgsT struct {
	acceptRoutes               bool
	acceptDNS                  bool
	exitNodeIP                 string
	exitNodeAllowLANAccess     bool
	shieldsUp                  bool
	runSSH                     bool
	runWebClient               bool
	hostname                   string
	advertiseRoutes            string
	advertiseDefaultRoute      bool
	advertiseConnector         bool
	opUser                     string
	acceptedRisks              string
	profileName                string
	forceDaemon                bool
	updateCheck                bool
	updateApply                bool
	reportPosture              bool
	remoteConfig               bool
	snat                       bool
	statefulFiltering          bool
	sync                       bool
	netfilterMode              string
	relayServerPort            string
	relayServerStaticEndpoints string
}

func newSetFlagSet(goos string, setArgs *setArgsT) *flag.FlagSet {
	setf := newFlagSet("set")

	setf.StringVar(&setArgs.profileName, "nickname", "", "当前账户的昵称")
	setf.BoolVar(&setArgs.acceptRoutes, "accept-routes", acceptRouteDefault(goos), "接受其他 Tailscale 节点广播的路由")
	setf.BoolVar(&setArgs.acceptDNS, "accept-dns", true, "接受来自管理后台的 DNS 配置")
	setf.StringVar(&setArgs.exitNodeIP, "exit-node", "", "用于互联网流量的 Tailscale 出口节点（IP、基础名称或 auto:any），或留空表示不使用出口节点")
	setf.BoolVar(&setArgs.exitNodeAllowLANAccess, "exit-node-allow-lan-access", false, "经由出口节点路由流量时，允许直接访问本地网络")
	setf.BoolVar(&setArgs.shieldsUp, "shields-up", false, "不允许入站连接")
	setf.BoolVar(&setArgs.runSSH, "ssh", false, "运行一个 SSH 服务器，按 tailnet 管理员声明的策略允许访问")
	setf.StringVar(&setArgs.hostname, "hostname", "", "用于替代操作系统提供的主机名")
	setf.StringVar(&setArgs.advertiseRoutes, "advertise-routes", "", "广播给其他节点的路由（逗号分隔，例如 \"10.0.0.0/8,192.168.0.0/24\"），或留空表示不广播路由")
	setf.BoolVar(&setArgs.advertiseDefaultRoute, "advertise-exit-node", false, "提供作为 tailnet 互联网流量的出口节点")
	setf.BoolVar(&setArgs.advertiseConnector, "advertise-connector", false, "提供作为针对特定域名互联网流量的应用连接器")
	setf.BoolVar(&setArgs.updateCheck, "update-check", true, "在有可用的 Tailscale 更新时通知")
	setf.BoolVar(&setArgs.updateApply, "auto-update", false, "自动更新到最新的可用版本")
	setf.BoolVar(&setArgs.reportPosture, "report-posture", false, "允许管理平面收集设备态势信息")
	setf.BoolVar(&setArgs.runWebClient, "webclient", false, "在 5252 端口暴露用于通过 Tailscale 管理此节点的 Web 界面")
	setf.BoolVar(&setArgs.remoteConfig, "remote-config", false, hidden+"将此节点的偏好设置与 LocalAPI 的完全远程控制权委托给 tailnet 管理员，绕过 Tailscale 逐项双重确认；仅当 tailnet 管理员拥有此机器或完全可信时使用")
	setf.BoolVar(&setArgs.sync, "sync", false, hidden+"主动从控制平面同步配置（仅在网络故障测试时设为 false）")
	setf.StringVar(&setArgs.relayServerPort, "relay-server-port", "", "中继服务器绑定的 UDP 端口号（在所有接口上，0 将随机选取一个未使用的端口），或留空以禁用中继服务器功能")
	setf.StringVar(&setArgs.relayServerStaticEndpoints, "relay-server-static-endpoints", "", "广播为中介连接候选的静态 IP:端口 端点（逗号分隔，例如 \"[2001:db8::1]:40000,192.0.2.1:40000\"），或留空表示不广播任何静态端点")

	ffcomplete.Flag(setf, "exit-node", func(args []string) ([]string, ffcomplete.ShellCompDirective, error) {
		st, err := localClient.Status(context.Background())
		if err != nil {
			return nil, 0, err
		}
		nodes := make([]string, 0, len(st.Peer))
		for _, node := range st.Peer {
			if !node.ExitNodeOption {
				continue
			}
			nodes = append(nodes, strings.TrimSuffix(node.DNSName, "."))
		}
		return nodes, ffcomplete.ShellCompDirectiveNoFileComp, nil
	})

	if safesocket.GOOSUsesPeerCreds(goos) {
		setf.StringVar(&setArgs.opUser, "operator", "", "允许在无 sudo 情况下操作 tailscaled 的 Unix 用户名")
	}
	switch goos {
	case "linux":
		setf.BoolVar(&setArgs.snat, "snat-subnet-routes", true, "对使用 --advertise-routes 广播的本地路由进行源地址 NAT")
		setf.BoolVar(&setArgs.statefulFiltering, "stateful-filtering", false, "对转发的包应用有状态过滤（子网路由器、出口节点等）")
		setf.StringVar(&setArgs.netfilterMode, "netfilter-mode", defaultNetfilterMode(), "netfilter 模式（on、nodivert、off 之一）")
	case "windows":
		setf.BoolVar(&setArgs.forceDaemon, "unattended", false, "以“无人值守模式”运行，即使当前 GUI 用户注销，Tailscale 仍保持运行（仅限 Windows）")
	}

	registerAcceptRiskFlag(setf, &setArgs.acceptedRisks)
	return setf
}

var (
	setArgs    setArgsT
	setFlagSet = newSetFlagSet(effectiveGOOS(), &setArgs)
)

func runSet(ctx context.Context, args []string) (retErr error) {
	if len(args) > 0 {
		fatalf("过多的非标志参数：%q", args)
	}

	st, err := localClient.Status(ctx)
	if err != nil {
		return err
	}

	// Note that even though we set the values here regardless of whether the
	// user passed the flag, the value is only used if the user passed the flag.
	// See updateMaskedPrefsFromUpOrSetFlag.
	maskedPrefs := &ipn.MaskedPrefs{
		Prefs: ipn.Prefs{
			ProfileName:            setArgs.profileName,
			RouteAll:               setArgs.acceptRoutes,
			CorpDNS:                setArgs.acceptDNS,
			ExitNodeAllowLANAccess: setArgs.exitNodeAllowLANAccess,
			ShieldsUp:              setArgs.shieldsUp,
			RunSSH:                 setArgs.runSSH,
			RunWebClient:           setArgs.runWebClient,
			Hostname:               setArgs.hostname,
			OperatorUser:           setArgs.opUser,
			NoSNAT:                 !setArgs.snat,
			ForceDaemon:            setArgs.forceDaemon,
			Sync:                   opt.NewBool(setArgs.sync),
			AutoUpdate: ipn.AutoUpdatePrefs{
				Check: setArgs.updateCheck,
				Apply: opt.NewBool(setArgs.updateApply),
			},
			AppConnector: ipn.AppConnectorPrefs{
				Advertise: setArgs.advertiseConnector,
			},
			PostureChecking:     setArgs.reportPosture,
			RemoteConfig:        setArgs.remoteConfig,
			NoStatefulFiltering: opt.NewBool(!setArgs.statefulFiltering),
		},
	}

	if effectiveGOOS() == "linux" {
		nfMode, warning, err := netfilterModeFromFlag(setArgs.netfilterMode)
		if err != nil {
			return err
		}
		if warning != "" {
			warnf(warning)
		}
		maskedPrefs.Prefs.NetfilterMode = nfMode
	}

	if setArgs.exitNodeIP != "" {
		if expr, useAutoExitNode := ipn.ParseAutoExitNodeString(setArgs.exitNodeIP); useAutoExitNode {
			maskedPrefs.AutoExitNode = expr
			maskedPrefs.AutoExitNodeSet = true
		} else if err := maskedPrefs.Prefs.SetExitNodeIP(setArgs.exitNodeIP, st); err != nil {
			if _, ok := errors.AsType[ipn.ExitNodeLocalIPError](err); ok {
				return fmt.Errorf("%w；您是否想使用 --advertise-exit-node？", err)
			}
			return err
		}
	}

	warnOnAdvertiseRoutes(ctx, &maskedPrefs.Prefs)

	var advertiseExitNodeSet, advertiseRoutesSet bool
	setFlagSet.Visit(func(f *flag.Flag) {
		updateMaskedPrefsFromUpOrSetFlag(maskedPrefs, f.Name)
		switch f.Name {
		case "advertise-exit-node":
			advertiseExitNodeSet = true
		case "advertise-routes":
			advertiseRoutesSet = true
		}
	})
	if maskedPrefs.IsEmpty() {
		return flag.ErrHelp
	}

	curPrefs, err := localClient.GetPrefs(ctx)
	if err != nil {
		return err
	}
	if maskedPrefs.AdvertiseRoutesSet {
		maskedPrefs.AdvertiseRoutes, err = calcAdvertiseRoutesForSet(advertiseExitNodeSet, advertiseRoutesSet, curPrefs, setArgs)
		if err != nil {
			return err
		}
	}

	if runtime.GOOS == "darwin" && maskedPrefs.AppConnector.Advertise {
		if err := presentRiskToUser(riskMacAppConnector, riskMacAppConnectorMessage, setArgs.acceptedRisks); err != nil {
			return err
		}
	}

	if maskedPrefs.RunSSHSet {
		wantSSH, haveSSH := maskedPrefs.RunSSH, curPrefs.RunSSH
		if err := presentSSHToggleRisk(wantSSH, haveSSH, setArgs.acceptedRisks); err != nil {
			return err
		}
	}
	if maskedPrefs.AutoUpdateSet.ApplySet && buildfeatures.HasClientUpdate && version.IsMacSysExt() {
		apply := "0"
		if maskedPrefs.AutoUpdate.Apply.EqualBool(true) {
			apply = "1"
		}
		out, err := exec.Command("defaults", "write", "io.tailscale.ipn.macsys", "SUAutomaticallyUpdate", apply).CombinedOutput()
		if err != nil {
			return fmt.Errorf("启用自动更新失败：%v，%q", err, out)
		}
	}

	if setArgs.relayServerPort != "" {
		uport, err := strconv.ParseUint(setArgs.relayServerPort, 10, 16)
		if err != nil {
			return fmt.Errorf("设置中继服务器端口失败：%v", err)
		}
		maskedPrefs.Prefs.RelayServerPort = new(uint16(uport))
	}

	if setArgs.relayServerStaticEndpoints != "" {
		endpointsSet := make(set.Set[netip.AddrPort])
		endpointsSplit := strings.SplitSeq(setArgs.relayServerStaticEndpoints, ",")
		for s := range endpointsSplit {
			ap, err := netip.ParseAddrPort(s)
			if err != nil {
				return fmt.Errorf("设置中继服务器静态端点失败：%q 不是有效的 IP:端口", s)
			}
			endpointsSet.Add(ap)
		}
		endpoints := endpointsSet.Slice()
		slices.SortFunc(endpoints, netip.AddrPort.Compare)
		maskedPrefs.Prefs.RelayServerStaticEndpoints = endpoints
	}

	checkPrefs := curPrefs.Clone()
	checkPrefs.ApplyEdits(maskedPrefs)
	// We want to make sure user is aware setting --snat-subnet-routes=false with --advertise-exit-node would break exitnode,
	// but we won't prevent them from doing it since there are current dependencies on that combination. (as of 2026-03-25)
	if checkPrefs.NoSNAT && checkPrefs.AdvertisesExitNode() {
		warnf("--snat-subnet-routes=false 与 --advertise-exit-node 同时设置；经此出口节点的互联网流量可能无法按预期工作")
	}
	if err := localClient.CheckPrefs(ctx, checkPrefs); err != nil {
		return err
	}

	_, err = localClient.EditPrefs(ctx, maskedPrefs)
	if err != nil {
		return err
	}

	if setArgs.runWebClient && len(st.TailscaleIPs) > 0 {
		printf("\nWeb 界面现已运行于 %s:%d\n", st.TailscaleIPs[0], tsconst.WebListenPort)
	}

	return nil
}

// calcAdvertiseRoutesForSet returns the new value for Prefs.AdvertiseRoutes based on the
// current value, the flags passed to "tailscale set".
// advertiseExitNodeSet is whether the --advertise-exit-node flag was set.
// advertiseRoutesSet is whether the --advertise-routes flag was set.
// curPrefs is the current Prefs.
// setArgs is the parsed command-line arguments.
func calcAdvertiseRoutesForSet(advertiseExitNodeSet, advertiseRoutesSet bool, curPrefs *ipn.Prefs, setArgs setArgsT) (routes []netip.Prefix, err error) {
	if advertiseExitNodeSet && advertiseRoutesSet {
		return netutil.CalcAdvertiseRoutes(setArgs.advertiseRoutes, setArgs.advertiseDefaultRoute)

	}
	if advertiseRoutesSet {
		return netutil.CalcAdvertiseRoutes(setArgs.advertiseRoutes, curPrefs.AdvertisesExitNode())
	}
	if advertiseExitNodeSet {
		alreadyAdvertisesExitNode := curPrefs.AdvertisesExitNode()
		if alreadyAdvertisesExitNode == setArgs.advertiseDefaultRoute {
			return curPrefs.AdvertiseRoutes, nil
		}
		routes = tsaddr.FilterPrefixesCopy(views.SliceOf(curPrefs.AdvertiseRoutes), func(p netip.Prefix) bool {
			return p.Bits() != 0
		})
		if setArgs.advertiseDefaultRoute {
			routes = append(routes, tsaddr.AllIPv4(), tsaddr.AllIPv6())
		}
		return routes, nil
	}
	return nil, nil
}
