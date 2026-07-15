// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"slices"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/types/appctype"
)

var appcRoutesArgs struct {
	all       bool
	domainMap bool
	n         bool
}

var appcRoutesCmd = &ffcli.Command{
	Name:       "appc-routes",
	ShortUsage: "tailscale appc-routes",
	Exec:       runAppcRoutesInfo,
	ShortHelp:  "打印当前的应用连接器路由",
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("appc-routes")
		fs.BoolVar(&appcRoutesArgs.all, "all", false, "打印已学习的域名、路由以及策略中额外配置的路由。")
		fs.BoolVar(&appcRoutesArgs.domainMap, "map", false, "打印已学习域名与路由的映射关系：[routes]。")
		fs.BoolVar(&appcRoutesArgs.n, "n", false, "打印此节点播发的路由总数。")
		return fs
	})(),
	LongHelp: strings.TrimSpace(`
'tailscale appc-routes' 命令用于打印当前 App Connector（应用连接器）的路由状态。

默认情况下，此命令会打印应用连接器配置中已配置的域名，以及为每个域名学习到的路由数量。

--all 会打印从应用连接器配置中已配置域名学习到的路由，以及策略中应用连接器 'routes' 字段提供的任何额外路由。

--map 会打印从应用连接器配置中已配置域名学习到的路由。

-n 会打印此设备播发的路由总数，无论是学习到的、在策略中设置的，还是在本地设置的。

有关 App Connectors 的更多信息，请参阅
https://tailscale.com/kb/1281/app-connectors
`),
}

func getAllOutput(ri *appctype.RouteInfo) (string, error) {
	domains, err := json.MarshalIndent(ri.Domains, " ", "  ")
	if err != nil {
		return "", err
	}
	control, err := json.MarshalIndent(ri.Control, " ", "  ")
	if err != nil {
		return "", err
	}
	s := fmt.Sprintf(`已学习路由
==============
%s

来自策略的路由
==================
%s
`, domains, control)
	return s, nil
}

type domainCount struct {
	domain string
	count  int
}

func getSummarizeLearnedOutput(ri *appctype.RouteInfo) string {
	x := make([]domainCount, len(ri.Domains))
	i := 0
	maxDomainWidth := 0
	for k, v := range ri.Domains {
		if len(k) > maxDomainWidth {
			maxDomainWidth = len(k)
		}
		x[i] = domainCount{domain: k, count: len(v)}
		i++
	}
	slices.SortFunc(x, func(i, j domainCount) int {
		if i.count > j.count {
			return -1
		}
		if i.count < j.count {
			return 1
		}
		if i.domain > j.domain {
			return 1
		}
		if i.domain < j.domain {
			return -1
		}
		return 0
	})
	var s strings.Builder
	fmtString := fmt.Sprintf("%%-%ds %%d\n", maxDomainWidth) // eg "%-10s %d\n"
	for _, dc := range x {
		s.WriteString(fmt.Sprintf(fmtString, dc.domain, dc.count))
	}
	return s.String()
}

func runAppcRoutesInfo(ctx context.Context, args []string) error {
	prefs, err := localClient.GetPrefs(ctx)
	if err != nil {
		return err
	}
	if !prefs.AppConnector.Advertise {
		fmt.Println("不是连接器")
		return nil
	}

	if appcRoutesArgs.n {
		fmt.Println(len(prefs.AdvertiseRoutes))
		return nil
	}

	routeInfo, err := localClient.GetAppConnectorRouteInfo(ctx)
	if err != nil {
		return err
	}

	if appcRoutesArgs.domainMap {
		domains, err := json.Marshal(routeInfo.Domains)
		if err != nil {
			return err
		}
		fmt.Println(string(domains))
		return nil
	}

	if appcRoutesArgs.all {
		s, err := getAllOutput(&routeInfo)
		if err != nil {
			return err
		}
		fmt.Println(s)
		return nil
	}

	fmt.Print(getSummarizeLearnedOutput(&routeInfo))
	return nil
}
