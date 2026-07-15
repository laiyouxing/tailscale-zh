// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_routecheck

package cli

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"

	jsonv2 "github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/peterbourgon/ff/v3/ffcli"

	"tailscale.com/cmd/tailscale/cli/jsonoutput"
	"tailscale.com/cmd/tailscale/tsroutecheckjsonv0"
	"tailscale.com/net/routecheck"
	"tailscale.com/tstime"
)

func init() {
	maybeRoutecheckCmd = routecheckCmd
}

var routecheckCmd = func() *ffcli.Command {
	return &ffcli.Command{
		Name:       "routecheck",
		ShortUsage: "tailscale routecheck",
		ShortHelp:  "为拥有多条路径的路由打印可达性报告",
		LongHelp:   hidden + `"tailscale routecheck" 是一项实验性功能，并非稳定接口`,
		Exec:       runRoutecheck,
		FlagSet:    routecheckFlagSet,
	}
}

var routecheckFlagSet = func() *flag.FlagSet {
	fs := newFlagSet("routecheck")
	fs.BoolVar(&routecheckArgs.probe, "probe", false, "立即探测以生成新的可达性报告")
	fs.Var(&routecheckArgs.format, "format", `输出格式：留空（人类可读），"json" 或 "json-line"`)
	fs.Var(routecheckArgs.format.JSONBool(), "json", "以 JSON 格式输出")
	return fs
}()

var routecheckArgs struct {
	probe  bool
	format jsonoutput.Format
}

func runRoutecheck(ctx context.Context, args []string) error {
	routeCheck := localClient.RouteCheck
	if routecheckArgs.probe {
		routeCheck = localClient.RouteCheckProbe
	}
	rp, err := routeCheck(ctx)
	if err != nil {
		return fmt.Errorf("routecheck: %w", err)
	}
	if err := printRouteCheckReport(rp); err != nil {
		return err
	}
	return nil
}

func printRouteCheckReport(rp *routecheck.Report) error {
	var enc *jsontext.Encoder
	switch routecheckArgs.format.String() {
	case "":
	case "json":
		enc = jsontext.NewEncoder(Stdout, jsontext.WithIndent("\t"))
	case "json-line":
		enc = jsontext.NewEncoder(Stdout, jsontext.Multiline(false))
	default:
		return fmt.Errorf("未知输出格式 %q", routecheckArgs.format)
	}

	if rp == nil {
		return fmt.Errorf("routecheck: 报告不可用")
	}
	routes := rp.RoutablePrefixes()

	// Don’t render prefixes that only have one router:
	for pfx, nodes := range routes {
		if len(nodes) <= 1 {
			delete(routes, pfx)
		}
	}

	if enc != nil {
		out := tsroutecheckjsonv0.ReportResponse{
			Done:   rp.Done,
			Routes: routes,
		}
		if err := jsonv2.MarshalEncode(enc, out); err != nil {
			return err
		}
		if _, err := Stdout.Write([]byte("\n")); err != nil {
			return err
		}
		return nil
	}

	w := tabwriter.NewWriter(Stdout, 10, 5, 5, ' ', 0)
	defer w.Flush()
	fmt.Fprintf(w, "\n在 %s 时可到达的路由器:\n", rp.Done.Local().Format(tstime.DateSpTimeZ))
	fmt.Fprintf(w, "\n %s\t%s\t%s", "前缀", "IP", "主机名")
	for prefix, nodes := range routes.Sorted() {
		slices.SortFunc(nodes, func(a, b routecheck.Node) int {
			return cmp.Compare(a.Name, b.Name) // order by hostname
		})
		for _, n := range nodes {
			fmt.Fprintf(w, "\n %s\t%s\t%s", prefix, n.Addr, strings.TrimSuffix(n.Name, "."))
		}
	}
	fmt.Fprintln(w)
	return nil
}
