// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"strings"
	"text/tabwriter"

	"github.com/peterbourgon/ff/v3/ffcli"
	"golang.org/x/net/dns/dnsmessage"
	"tailscale.com/cmd/tailscale/cli/jsonoutput"
)

var dnsQueryArgs struct {
	json bool
}

var dnsQueryCmd = &ffcli.Command{
	Name:       "query",
	ShortUsage: "tailscale dns query [--json] <name> [type]",
	Exec:       runDNSQuery,
	ShortHelp:  "执行一次 DNS 查询",
	LongHelp: strings.TrimSpace(`
'tailscale dns query' 子命令使用内部 DNS 转发器（100.100.100.100）
对指定的名称执行一次 DNS 查询。

默认情况下，DNS 查询会请求一条 A 记录。可将记录类型作为名称后的
第二个参数指定（如 AAAA、CNAME、MX、NS、PTR、SRV、TXT）。

输出还会提供用于解析该查询的解析器（resolver）的相关信息。
`),
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("query")
		fs.BoolVar(&dnsQueryArgs.json, "json", false, "以 JSON 格式输出")
		return fs
	})(),
}

func runDNSQuery(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("缺少必需参数：名称")
	}
	if len(args) > 1 {
		var flags []string
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "-") {
				flags = append(flags, a)
			}
		}
		if len(flags) > 0 {
			return fmt.Errorf("查询名称后出现意外的标志：%s；请参见 'tailscale dns query --help'", strings.Join(flags, ", "))
		}
		if len(args) > 2 {
			return fmt.Errorf("意外的多余参数：%s", strings.Join(args[2:], " "))
		}
	}
	name := args[0]
	queryType := "A"
	if len(args) > 1 {
		queryType = strings.ToUpper(args[1])
	}

	rawBytes, resolvers, err := localClient.QueryDNS(ctx, name, queryType)
	if err != nil {
		return fmt.Errorf("查询 DNS 失败：%w", err)
	}

	data := &jsonoutput.DNSQueryResult{
		Name:      name,
		QueryType: queryType,
	}

	for _, r := range resolvers {
		data.Resolvers = append(data.Resolvers, makeDNSResolverInfo(r))
	}

	var p dnsmessage.Parser
	header, err := p.Start(rawBytes)
	if err != nil {
		return fmt.Errorf("解析 DNS 响应失败：%w", err)
	}
	data.ResponseCode = header.RCode.String()

	p.SkipAllQuestions()

	if header.RCode == dnsmessage.RCodeSuccess {
		answers, err := p.AllAnswers()
		if err != nil {
			return fmt.Errorf("解析 DNS 应答失败：%w", err)
		}
		data.Answers = make([]jsonoutput.DNSAnswer, 0, len(answers))
		for _, a := range answers {
			data.Answers = append(data.Answers, jsonoutput.DNSAnswer{
				Name:  a.Header.Name.String(),
				TTL:   a.Header.TTL,
				Class: a.Header.Class.String(),
				Type:  a.Header.Type.String(),
				Body:  makeAnswerBody(a),
			})
		}
	}

	if dnsQueryArgs.json {
		j, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return err
		}
		printf("%s\n", j)
		return nil
	}
	printf("%s", formatDNSQueryText(data))
	return nil
}

func formatDNSQueryText(data *jsonoutput.DNSQueryResult) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "使用内部解析器对 %q（%s）的 DNS 查询：\n", data.Name, data.QueryType)
	fmt.Fprintf(&sb, "\n")
	if len(data.Resolvers) == 1 {
		fmt.Fprintf(&sb, "转发到解析器：%v\n", formatResolverString(data.Resolvers[0]))
	} else {
		fmt.Fprintf(&sb, "有多个可用解析器：\n")
		for _, r := range data.Resolvers {
			fmt.Fprintf(&sb, "  - %v\n", formatResolverString(r))
		}
	}
	fmt.Fprintf(&sb, "\n")
	fmt.Fprintf(&sb, "响应码：%v\n", data.ResponseCode)
	fmt.Fprintf(&sb, "\n")

	if data.Answers == nil {
		fmt.Fprintf(&sb, "未返回任何应答。\n")
		return sb.String()
	}

	if len(data.Answers) == 0 {
		fmt.Fprintf(&sb, "  （未找到应答）\n")
	}

	w := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "名称\tTTL\t类别\t类型\t内容")
	fmt.Fprintln(w, "----\t---\t-----\t----\t----")
	for _, a := range data.Answers {
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n", a.Name, a.TTL, a.Class, a.Type, a.Body)
	}
	w.Flush()

	fmt.Fprintf(&sb, "\n")
	return sb.String()
}

// formatResolverString formats a jsonoutput.DNSResolverInfo for human-readable text output.
func formatResolverString(r jsonoutput.DNSResolverInfo) string {
	if len(r.BootstrapResolution) > 0 {
		return fmt.Sprintf("%s (bootstrap: %v)", r.Addr, r.BootstrapResolution)
	}
	return r.Addr
}

// makeAnswerBody returns a string with the DNS answer body in a human-readable format.
func makeAnswerBody(a dnsmessage.Resource) string {
	switch a.Header.Type {
	case dnsmessage.TypeA:
		return makeABody(a.Body)
	case dnsmessage.TypeAAAA:
		return makeAAAABody(a.Body)
	case dnsmessage.TypeCNAME:
		return makeCNAMEBody(a.Body)
	case dnsmessage.TypeMX:
		return makeMXBody(a.Body)
	case dnsmessage.TypeNS:
		return makeNSBody(a.Body)
	case dnsmessage.TypeOPT:
		return makeOPTBody(a.Body)
	case dnsmessage.TypePTR:
		return makePTRBody(a.Body)
	case dnsmessage.TypeSRV:
		return makeSRVBody(a.Body)
	case dnsmessage.TypeTXT:
		return makeTXTBody(a.Body)
	default:
		return a.Body.GoString()
	}
}

func makeABody(a dnsmessage.ResourceBody) string {
	if a, ok := a.(*dnsmessage.AResource); ok {
		return netip.AddrFrom4(a.A).String()
	}
	return ""
}
func makeAAAABody(aaaa dnsmessage.ResourceBody) string {
	if a, ok := aaaa.(*dnsmessage.AAAAResource); ok {
		return netip.AddrFrom16(a.AAAA).String()
	}
	return ""
}
func makeCNAMEBody(cname dnsmessage.ResourceBody) string {
	if c, ok := cname.(*dnsmessage.CNAMEResource); ok {
		return c.CNAME.String()
	}
	return ""
}
func makeMXBody(mx dnsmessage.ResourceBody) string {
	if m, ok := mx.(*dnsmessage.MXResource); ok {
		return fmt.Sprintf("%s (Priority=%d)", m.MX, m.Pref)
	}
	return ""
}
func makeNSBody(ns dnsmessage.ResourceBody) string {
	if n, ok := ns.(*dnsmessage.NSResource); ok {
		return n.NS.String()
	}
	return ""
}
func makeOPTBody(opt dnsmessage.ResourceBody) string {
	if o, ok := opt.(*dnsmessage.OPTResource); ok {
		return o.GoString()
	}
	return ""
}
func makePTRBody(ptr dnsmessage.ResourceBody) string {
	if p, ok := ptr.(*dnsmessage.PTRResource); ok {
		return p.PTR.String()
	}
	return ""
}
func makeSRVBody(srv dnsmessage.ResourceBody) string {
	if s, ok := srv.(*dnsmessage.SRVResource); ok {
		return fmt.Sprintf("Target=%s, Port=%d, Priority=%d, Weight=%d", s.Target.String(), s.Port, s.Priority, s.Weight)
	}
	return ""
}
func makeTXTBody(txt dnsmessage.ResourceBody) string {
	if t, ok := txt.(*dnsmessage.TXTResource); ok {
		return fmt.Sprintf("%q", t.TXT)
	}
	return ""
}
