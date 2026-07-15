// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/envknob"
	"tailscale.com/feature/buildfeatures"
	"tailscale.com/ipn"
	"tailscale.com/net/netcheck"
	"tailscale.com/net/netmon"
	"tailscale.com/net/portmapper/portmappertype"
	"tailscale.com/net/tlsdial"
	"tailscale.com/tailcfg"
	"tailscale.com/tstime"
	"tailscale.com/types/logger"
	"tailscale.com/util/eventbus"
	"tailscale.com/util/set"

	// The "netcheck" command also wants the portmapper linked.
	//
	// TODO: make that subcommand either hit LocalAPI for that info, or use a
	// tailscaled subcommand, to avoid making the CLI also link in the portmapper.
	// For now (2025-09-15), keep doing what we've done for the past five years and
	// keep linking it here.
	_ "tailscale.com/feature/condregister/portmapper"
)

var netcheckCmd = &ffcli.Command{
	Name:       "netcheck",
	ShortUsage: "tailscale netcheck",
	ShortHelp:  "打印本地网络状况分析",
	Exec:       runNetcheck,
	FlagSet:    netcheckFlagSet,
}

var netcheckFlagSet = func() *flag.FlagSet {
	fs := newFlagSet("netcheck")
	fs.StringVar(&netcheckArgs.format, "format", "", `输出格式；为空（人类可读）、"json" 或 "json-line"`)
	fs.DurationVar(&netcheckArgs.every, "every", 0, "若非 0，则按给定频率进行增量报告")
	fs.BoolVar(&netcheckArgs.verbose, "verbose", false, "详细日志")
	fs.StringVar(&netcheckArgs.bindAddress, "bind-address", "", "使用此本地绑定 IP 地址发送和接收连通性探测；默认：由操作系统分配")
	fs.IntVar(&netcheckArgs.bindPort, "bind-port", 0, "使用此 UDP 端口发送和接收连通性探测；默认：由操作系统分配")
	return fs
}()

var netcheckArgs struct {
	format      string
	every       time.Duration
	verbose     bool
	bindAddress string
	bindPort    int
}

func runNetcheck(ctx context.Context, args []string) error {
	logf := logger.WithPrefix(log.Printf, "portmap: ")
	bus := eventbus.New()
	defer bus.Close()
	netMon, err := netmon.New(bus, logf)
	if err != nil {
		return err
	}

	var pm portmappertype.Client
	if buildfeatures.HasPortMapper {
		// Ensure that we close the portmapper after running a netcheck; this
		// will release any port mappings created.
		pm = portmappertype.HookNewPortMapper.Get()(logf, bus, netMon, nil, nil)
		defer pm.Close()
	}

	flagsProvided := set.Set[string]{}
	netcheckFlagSet.Visit(func(f *flag.Flag) {
		flagsProvided.Add(f.Name)
	})

	c := &netcheck.Client{
		NetMon:      netMon,
		PortMapper:  pm,
		UseDNSCache: false, // always resolve, don't cache
	}
	if netcheckArgs.verbose {
		c.Logf = logger.WithPrefix(log.Printf, "netcheck: ")
		c.Verbose = true
	} else {
		c.Logf = logger.Discard
	}

	if strings.HasPrefix(netcheckArgs.format, "json") {
		fmt.Fprintln(Stderr, "# 警告：此 JSON 格式尚未被视为稳定接口")
	}

	bind, err := createNetcheckBindString(
		netcheckArgs.bindAddress,
		flagsProvided.Contains("bind-address"),
		netcheckArgs.bindPort,
		flagsProvided.Contains("bind-port"),
		envknob.String("TS_DEBUG_NETCHECK_UDP_BIND"))
	if err != nil {
		return err
	}

	if err := c.Standalone(ctx, bind); err != nil {
		fmt.Fprintln(Stderr, "netcheck：UDP 测试失败：", err)
	}

	dm, err := localClient.CurrentDERPMap(ctx)
	noRegions := dm != nil && len(dm.Regions) == 0
	if noRegions {
		log.Printf("tailscaled 未提供 DERP 映射；使用默认映射。")
	}
	if err != nil || noRegions {
		hc := &http.Client{
			Transport: tlsdial.NewTransport(),
			Timeout:   10 * time.Second,
		}
		dm, err = prodDERPMap(ctx, hc)
		if err != nil {
			log.Println("获取 DERP 映射失败，netcheck 无法继续。请检查你的网络连接。")
			return err
		}
	}
	for {
		t0 := time.Now()
		report, err := c.GetReport(ctx, dm, nil)
		d := time.Since(t0)
		if netcheckArgs.verbose {
			c.Logf("GetReport took %v; err=%v", d.Round(time.Millisecond), err)
		}
		if err != nil {
			return fmt.Errorf("netcheck：%w", err)
		}
		if err := printNetCheckReport(dm, report); err != nil {
			return err
		}
		if netcheckArgs.every == 0 {
			return nil
		}
		time.Sleep(netcheckArgs.every)
	}
}

func printNetCheckReport(dm *tailcfg.DERPMap, report *netcheck.Report) error {
	var j []byte
	var err error
	switch netcheckArgs.format {
	case "":
	case "json":
		j, err = json.MarshalIndent(report, "", "\t")
	case "json-line":
		j, err = json.Marshal(report)
	default:
		return fmt.Errorf("未知的输出格式 %q", netcheckArgs.format)
	}
	if err != nil {
		return err
	}
	if j != nil {
		j = append(j, '\n')
		Stdout.Write(j)
		return nil
	}

	printf("\n报告：\n")
	printf("\t* 时间：%v\n", report.Now.Local().Format(tstime.DateSpTimeNanoZ))
	printf("\t* UDP：%v\n", report.UDP)
	if report.GlobalV4.IsValid() {
		printf("\t* IPv4：是，%s\n", report.GlobalV4)
	} else {
		printf("\t* IPv4：（未找到地址）\n")
	}
	if report.GlobalV6.IsValid() {
		printf("\t* IPv6：是，%s\n", report.GlobalV6)
	} else if report.IPv6 {
		printf("\t* IPv6：（未找到地址）\n")
	} else if report.OSHasIPv6 {
		printf("\t* IPv6：否，但操作系统支持\n")
	} else {
		printf("\t* IPv6：否，操作系统不支持\n")
	}
	printf("\t* 映射是否随目标 IP 变化：%v\n", report.MappingVariesByDestIP)
	printf("\t* 端口映射：%v\n", portMapping(report))
	if report.CaptivePortal != "" {
		printf("\t* 强制门户：%v\n", report.CaptivePortal)
	}

	// When DERP latency checking failed,
	// magicsock will try to pick the DERP server that
	// most of your other nodes are also using
	if len(report.RegionLatency) == 0 {
		printf("\t* 最近的 DERP：未知（延迟探测无响应）\n")
	} else {
		if report.PreferredDERP != 0 {
			if region, ok := dm.Regions[report.PreferredDERP]; ok {
				printf("\t* 最近的 DERP：%v\n", region.RegionName)
			} else {
				printf("\t* 最近的 DERP：%v（在映射中未找到该区域）\n", report.PreferredDERP)
			}
		} else {
			printf("\t* 最近的 DERP：[无]\n")
		}
		printf("\t* DERP 延迟：\n")
		var rids []int
		for rid := range dm.Regions {
			rids = append(rids, rid)
		}
		sort.Slice(rids, func(i, j int) bool {
			l1, ok1 := report.RegionLatency[rids[i]]
			l2, ok2 := report.RegionLatency[rids[j]]
			if ok1 != ok2 {
				return ok1 // defined things sort first
			}
			if !ok1 {
				return rids[i] < rids[j]
			}
			return l1 < l2
		})
		for _, rid := range rids {
			d, ok := report.RegionLatency[rid]
			var latency string
			if ok {
				latency = d.Round(time.Millisecond / 10).String()
			}
			r := dm.Regions[rid]
			var derpNum string
			if netcheckArgs.verbose {
				derpNum = fmt.Sprintf("derp%d, ", rid)
			}
			printf("\t\t- %3s: %-7s (%s%s)\n", r.RegionCode, latency, derpNum, r.RegionName)
		}
	}
	return nil
}

func portMapping(r *netcheck.Report) string {
	if !buildfeatures.HasPortMapper {
		return "该二进制未包含端口映射支持"
	}
	if !r.AnyPortMappingChecked() {
		return "未检查"
	}
	var got []string
	if r.UPnP.EqualBool(true) {
		got = append(got, "UPnP")
	}
	if r.PMP.EqualBool(true) {
		got = append(got, "NAT-PMP")
	}
	if r.PCP.EqualBool(true) {
		got = append(got, "PCP")
	}
	return strings.Join(got, ", ")
}

func prodDERPMap(ctx context.Context, httpc *http.Client) (*tailcfg.DERPMap, error) {
	log.Printf("正在尝试从 %s 获取 DERPMap", ipn.DefaultControlURL)
	req, err := http.NewRequestWithContext(ctx, "GET", ipn.DefaultControlURL+"/derpmap/default", nil)
	if err != nil {
		return nil, fmt.Errorf("创建 prodDERPMap 请求失败：%w", err)
	}
	res, err := httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取 prodDERPMap 失败：%w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("获取 prodDERPMap 失败：%w", err)
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("获取 prodDERPMap：%v：%s", res.Status, b)
	}
	var derpMap tailcfg.DERPMap
	if err = json.Unmarshal(b, &derpMap); err != nil {
		return nil, fmt.Errorf("获取 prodDERPMap 失败：%w", err)
	}
	return &derpMap, nil
}

// createNetcheckBindString determines the netcheck socket bind "address:port" string based
// on the CLI args and environment variable values used to invoke the netcheck CLI.
// Arguments cliAddressIsSet and cliPortIsSet explicitly indicate whether the
// corresponding cliAddress and cliPort were set in CLI args, instead of relying
// on in-band sentinel values.
func createNetcheckBindString(cliAddress string, cliAddressIsSet bool, cliPort int, cliPortIsSet bool, envBind string) (string, error) {
	// Default to port number 0 but overwrite with a valid CLI value, if set.
	var port uint16 = 0
	if cliPortIsSet {
		// 0 is valid, results in OS picking port.
		if cliPort >= 0 && cliPort <= math.MaxUint16 {
			port = uint16(cliPort)
		} else {
			return "", fmt.Errorf("无效的绑定端口号：%d", cliPort)
		}
	}

	// Use CLI address, if set.
	if cliAddressIsSet {
		addr, err := netip.ParseAddr(cliAddress)
		if err != nil {
			return "", fmt.Errorf("无效的绑定地址：%q", cliAddress)
		}
		return netip.AddrPortFrom(addr, port).String(), nil
	} else {
		// No CLI address set, but port is set.
		if cliPortIsSet {
			return fmt.Sprintf(":%d", port), nil
		}
	}

	// Fall back to the environment variable.
	// Intentionally skipping input validation here to avoid breaking legacy usage method.
	if envBind != "" {
		return envBind, nil
	}

	// OS picks both address and port.
	return ":0", nil
}
