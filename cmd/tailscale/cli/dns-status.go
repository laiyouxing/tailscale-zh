// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/cmd/tailscale/cli/jsonoutput"
	"tailscale.com/types/dnstype"
)

var dnsStatusCmd = &ffcli.Command{
	Name:       "status",
	ShortUsage: "tailscale dns status [--all] [--json]",
	Exec:       runDNSStatus,
	ShortHelp:  "打印当前 DNS 状态与配置",
	LongHelp: strings.TrimSpace(`
'tailscale dns status' 子命令打印当前的 DNS 状态与配置，包括：

- 内置 DNS 转发器是否已启用。

- 由协调服务器提供的 MagicDNS 配置。

- Tailscale 认为系统默认正在使用哪些解析器的详细信息。

--all 标志可用于输出高级调试信息，包括回退解析器、名称服务器、证书域、
额外记录以及出口节点过滤集合。

=== MagicDNS 配置的内容 ===

MagicDNS 配置由协调服务器提供给客户端，包含以下组成部分：

- MagicDNS 启用状态：表示 MagicDNS 是否在整条 tailnet 上启用。

- MagicDNS 后缀：你的 tailnet 内设备使用的 DNS 后缀。

- DNS 名称：tailnet 中其他设备用来访问本设备的 DNS 名称。

- 解析器：用于解析查询的优选 DNS 解析器，按优先级排列。若此处未列出任何
  解析器，则使用系统默认值。

- 拆分 DNS 路由：可使用自定义 DNS 解析器来解析特定域中的主机名，这也被称为
  "拆分 DNS" 配置。此处提供域到各自解析器的映射。

- 证书域：协调服务器将协助为其配置 TLS 证书的 DNS 名称。

- 额外记录：协调服务器可能提供给内部 DNS 解析器的额外 DNS 记录。

- 出口节点过滤集合：本节点作为出口节点 DNS 代理时不会应答的 DNS 后缀。

有关 Tailscale 内置 DNS 功能的更多信息，请参阅
https://tailscale.com/kb/1054/dns。
`),
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("status")
		fs.BoolVar(&dnsStatusArgs.all, "all", false, "输出高级调试信息")
		fs.BoolVar(&dnsStatusArgs.json, "json", false, "以 JSON 格式输出")
		return fs
	})(),
}

// dnsStatusArgs are the arguments for the "dns status" subcommand.
var dnsStatusArgs struct {
	all  bool
	json bool
}

// makeDNSResolverInfo converts a dnstype.Resolver to a jsonoutput.DNSResolverInfo.
func makeDNSResolverInfo(r *dnstype.Resolver) jsonoutput.DNSResolverInfo {
	info := jsonoutput.DNSResolverInfo{Addr: r.Addr}
	if r.BootstrapResolution != nil {
		info.BootstrapResolution = make([]string, 0, len(r.BootstrapResolution))
		for _, a := range r.BootstrapResolution {
			info.BootstrapResolution = append(info.BootstrapResolution, a.String())
		}
	}
	return info
}

func runDNSStatus(ctx context.Context, args []string) error {
	s, err := localClient.Status(ctx)
	if err != nil {
		return err
	}

	prefs, err := localClient.GetPrefs(ctx)
	if err != nil {
		return err
	}

	data := &jsonoutput.DNSStatusResult{
		TailscaleDNS: prefs.CorpDNS,
	}

	if s.CurrentTailnet != nil {
		data.CurrentTailnet = &jsonoutput.DNSTailnetInfo{
			MagicDNSEnabled: s.CurrentTailnet.MagicDNSEnabled,
			MagicDNSSuffix:  s.CurrentTailnet.MagicDNSSuffix,
			SelfDNSName:     s.Self.DNSName,
		}

		dnsConfig, err := localClient.DNSConfig(ctx)
		if err != nil {
			return fmt.Errorf("获取 DNS 配置失败：%w", err)
		}

		for _, r := range dnsConfig.Resolvers {
			data.Resolvers = append(data.Resolvers, makeDNSResolverInfo(r))
		}

		data.SplitDNSRoutes = make(map[string][]jsonoutput.DNSResolverInfo)
		for k, v := range dnsConfig.Routes {
			for _, r := range v {
				data.SplitDNSRoutes[k] = append(data.SplitDNSRoutes[k], makeDNSResolverInfo(r))
			}
		}

		for _, r := range dnsConfig.FallbackResolvers {
			data.FallbackResolvers = append(data.FallbackResolvers, makeDNSResolverInfo(r))
		}

		domains := slices.Clone(dnsConfig.Domains)
		slices.Sort(domains)
		data.SearchDomains = domains

		for _, a := range dnsConfig.Nameservers {
			data.Nameservers = append(data.Nameservers, a.String())
		}

		data.CertDomains = dnsConfig.CertDomains

		for _, er := range dnsConfig.ExtraRecords {
			data.ExtraRecords = append(data.ExtraRecords, jsonoutput.DNSExtraRecord{
				Name:  er.Name,
				Type:  er.Type,
				Value: er.Value,
			})
		}

		data.ExitNodeFilteredSet = dnsConfig.ExitNodeFilteredSet

		osCfg, err := localClient.GetDNSOSConfig(ctx)
		if err != nil {
			if strings.Contains(err.Error(), "not supported") {
				data.SystemDNSError = "此平台不支持"
			} else {
				data.SystemDNSError = err.Error()
			}
		} else if osCfg != nil {
			data.SystemDNS = &jsonoutput.DNSSystemConfig{
				Nameservers:   osCfg.Nameservers,
				SearchDomains: osCfg.SearchDomains,
				MatchDomains:  osCfg.MatchDomains,
			}
		}
	}

	if dnsStatusArgs.json {
		j, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return err
		}
		printf("%s\n", j)
		return nil
	}
	printf("%s", formatDNSStatusText(data, dnsStatusArgs.all))
	return nil
}

func formatDNSStatusText(data *jsonoutput.DNSStatusResult, all bool) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "\n")
	fmt.Fprintf(&sb, "=== \"使用 Tailscale DNS\" 状态 ===\n")
	fmt.Fprintf(&sb, "\n")
	if data.TailscaleDNS {
		fmt.Fprintf(&sb, "Tailscale DNS：已启用。\n\nTailscale 已配置为在本设备上处理 DNS 查询。\n运行 'tailscale set --accept-dns=false' 可恢复为系统默认 DNS 解析器。\n")
	} else {
		fmt.Fprintf(&sb, "Tailscale DNS：已禁用。\n\n（运行 'tailscale set --accept-dns=true' 以开始将 DNS 查询发往 Tailscale DNS 解析器）\n")
	}
	fmt.Fprintf(&sb, "\n")
	fmt.Fprintf(&sb, "=== MagicDNS 配置 ===\n")
	fmt.Fprintf(&sb, "\n")
	fmt.Fprintf(&sb, "这是由协调服务器提供给本设备的 DNS 配置。\n")
	fmt.Fprintf(&sb, "\n")
	if data.CurrentTailnet == nil {
		fmt.Fprintf(&sb, "没有可用的 tailnet 信息；请确保你已登录到某条 tailnet。\n")
		return sb.String()
	}

	if data.CurrentTailnet.MagicDNSEnabled {
		fmt.Fprintf(&sb, "MagicDNS：整条 tailnet 已启用（后缀 = %s）", data.CurrentTailnet.MagicDNSSuffix)
		fmt.Fprintf(&sb, "\n\n")
		fmt.Fprintf(&sb, "你的 tailnet 中其他设备可在 %s 访问本设备\n", data.CurrentTailnet.SelfDNSName)
	} else {
		fmt.Fprintf(&sb, "MagicDNS：整条 tailnet 已禁用。\n")
	}
	fmt.Fprintf(&sb, "\n")

	fmt.Fprintf(&sb, "解析器（按优先级）：\n")
	if len(data.Resolvers) == 0 {
		fmt.Fprintf(&sb, "  （未配置解析器，将使用系统默认值：见下文\"系统 DNS 配置\"）\n")
	}
	for _, r := range data.Resolvers {
		fmt.Fprintf(&sb, "  - %v", r.Addr)
		if r.BootstrapResolution != nil {
			fmt.Fprintf(&sb, "（bootstrap：%v）", r.BootstrapResolution)
		}
		fmt.Fprintf(&sb, "\n")
	}
	fmt.Fprintf(&sb, "\n")

	fmt.Fprintf(&sb, "拆分 DNS 路由：\n")
	if len(data.SplitDNSRoutes) == 0 {
		fmt.Fprintf(&sb, "  （未配置路由：拆分 DNS 已禁用）\n")
	}
	for _, k := range slices.Sorted(maps.Keys(data.SplitDNSRoutes)) {
		for _, r := range data.SplitDNSRoutes[k] {
			fmt.Fprintf(&sb, "  - %-30s -> %v", k, r.Addr)
			if r.BootstrapResolution != nil {
				fmt.Fprintf(&sb, "（bootstrap：%v）", r.BootstrapResolution)
			}
			fmt.Fprintf(&sb, "\n")
		}
	}
	fmt.Fprintf(&sb, "\n")

	if all {
		fmt.Fprintf(&sb, "回退解析器：\n")
		if len(data.FallbackResolvers) == 0 {
			fmt.Fprintf(&sb, "  （未配置回退解析器）\n")
		}
		for i, r := range data.FallbackResolvers {
			fmt.Fprintf(&sb, "  %d: %v", i, r.Addr)
			if r.BootstrapResolution != nil {
				fmt.Fprintf(&sb, "（bootstrap：%v）", r.BootstrapResolution)
			}
			fmt.Fprintf(&sb, "\n")
		}
		fmt.Fprintf(&sb, "\n")
	}

	fmt.Fprintf(&sb, "搜索域：\n")
	if len(data.SearchDomains) == 0 {
		fmt.Fprintf(&sb, "  （未配置搜索域）\n")
	}
	for _, r := range data.SearchDomains {
		fmt.Fprintf(&sb, "  - %v\n", r)
	}
	fmt.Fprintf(&sb, "\n")

	if all {
		fmt.Fprintf(&sb, "名称服务器 IP 地址：\n")
		if len(data.Nameservers) == 0 {
			fmt.Fprintf(&sb, "  （未提供任何地址）\n")
		}
		for _, r := range data.Nameservers {
			fmt.Fprintf(&sb, "  - %v\n", r)
		}
		fmt.Fprintf(&sb, "\n")

		fmt.Fprintf(&sb, "证书域：\n")
		if len(data.CertDomains) == 0 {
			fmt.Fprintf(&sb, "  （未配置证书域）\n")
		}
		for _, r := range data.CertDomains {
			fmt.Fprintf(&sb, "  - %v\n", r)
		}
		fmt.Fprintf(&sb, "\n")

		fmt.Fprintf(&sb, "额外 DNS 记录：\n")
		if len(data.ExtraRecords) == 0 {
			fmt.Fprintf(&sb, "  （未配置额外记录）\n")
		}
		for _, er := range data.ExtraRecords {
			if er.Type == "" {
				fmt.Fprintf(&sb, "  - %-50s -> %v\n", er.Name, er.Value)
			} else {
				fmt.Fprintf(&sb, "  - [%s] %-50s -> %v\n", er.Type, er.Name, er.Value)
			}
		}
		fmt.Fprintf(&sb, "\n")

		fmt.Fprintf(&sb, "作为出口节点转发 DNS 查询时被过滤的后缀：\n")
		if len(data.ExitNodeFilteredSet) == 0 {
			fmt.Fprintf(&sb, "  （未过滤任何后缀）\n")
		}
		for _, s := range data.ExitNodeFilteredSet {
			fmt.Fprintf(&sb, "  - %s\n", s)
		}
		fmt.Fprintf(&sb, "\n")
	}

	fmt.Fprintf(&sb, "=== 系统 DNS 配置 ===\n")
	fmt.Fprintf(&sb, "\n")
	fmt.Fprintf(&sb, "这是 Tailscale 认为你的操作系统正在使用的 DNS 配置。\n若管理控制台中\"覆盖本地 DNS\"被禁用，或协调服务器未提供任何解析器，\nTailscale 可能会使用此配置。\n")
	fmt.Fprintf(&sb, "\n")

	if data.SystemDNSError != "" {
		if strings.Contains(data.SystemDNSError, "not supported") {
			fmt.Fprintf(&sb, "  （此平台不支持读取系统 DNS 配置）\n")
		} else {
			fmt.Fprintf(&sb, "  （读取系统 DNS 配置失败：%s）\n", data.SystemDNSError)
		}
	} else if data.SystemDNS == nil {
		fmt.Fprintf(&sb, "  （没有可用的操作系统 DNS 配置）\n")
	} else {
		fmt.Fprintf(&sb, "名称服务器：\n")
		if len(data.SystemDNS.Nameservers) == 0 {
			fmt.Fprintf(&sb, "  （未找到名称服务器，DNS 查询可能失败\n除非协调服务器提供了名称服务器）\n")
		}
		for _, ns := range data.SystemDNS.Nameservers {
			fmt.Fprintf(&sb, "  - %v\n", ns)
		}
		fmt.Fprintf(&sb, "\n")
		fmt.Fprintf(&sb, "搜索域：\n")
		if len(data.SystemDNS.SearchDomains) == 0 {
			fmt.Fprintf(&sb, "  （未找到搜索域）\n")
		}
		for _, sd := range data.SystemDNS.SearchDomains {
			fmt.Fprintf(&sb, "  - %v\n", sd)
		}
		if all {
			fmt.Fprintf(&sb, "\n")
			fmt.Fprintf(&sb, "匹配域：\n")
			if len(data.SystemDNS.MatchDomains) == 0 {
				fmt.Fprintf(&sb, "  （未找到匹配域）\n")
			}
			for _, md := range data.SystemDNS.MatchDomains {
				fmt.Fprintf(&sb, "  - %v\n", md)
			}
		}
	}
	fmt.Fprintf(&sb, "\n")
	fmt.Fprintf(&sb, "[这是该命令的预览版本；输出格式未来可能会发生变化]\n")
	return sb.String()
}
