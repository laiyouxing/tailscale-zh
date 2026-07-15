// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/peterbourgon/ff/v3/ffcli"
	"golang.org/x/net/idna"
	"tailscale.com/feature"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/net/netmon"
	"tailscale.com/util/dnsname"
)

var statusCmd = &ffcli.Command{
	Name:       "status",
	ShortUsage: "tailscale status [--active] [--web] [--json]",
	ShortHelp:  "显示 tailscaled 及其连接的状态",
	LongHelp: strings.TrimSpace(`

JSON 格式

警告：该格式在不同版本之间发生过变化，未来可能还会变化。

各字段的说明，参见 "type Status" 声明：

https://github.com/tailscale/tailscale/blob/main/ipn/ipnstate/ipnstate.go

（请确保选择的分支/标签与你运行的 Tailscale 版本相对应）

`),
	Exec: runStatus,
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("status")
		fs.BoolVar(&statusArgs.json, "json", false, "以 JSON 格式输出（警告：输出格式可能发生变化）")
		fs.BoolVar(&statusArgs.web, "web", false, "运行一个展示状态的 Web 服务器（HTML）")
		fs.BoolVar(&statusArgs.active, "active", false, "仅筛选具有活动会话的对等节点输出（不适用于 web 模式）")
		fs.BoolVar(&statusArgs.self, "self", true, "显示本机状态")
		fs.BoolVar(&statusArgs.peers, "peers", true, "显示对等节点状态")
		fs.StringVar(&statusArgs.listen, "listen", "127.0.0.1:8384", "web 模式下的监听地址；使用端口 0 表示自动分配")
		fs.BoolVar(&statusArgs.browser, "browser", true, "在 web 模式下打开浏览器")
		fs.BoolVar(&statusArgs.header, "header", false, "以表格格式显示列标题")
		return fs
	})(),
}

var statusArgs struct {
	json    bool   // JSON output mode
	web     bool   // run webserver
	listen  string // in web mode, webserver address to listen on, empty means auto
	browser bool   // in web mode, whether to open browser
	active  bool   // in CLI mode, filter output to only peers with active sessions
	self    bool   // in CLI mode, show status of local machine
	peers   bool   // in CLI mode, show status of peer machines
	header  bool   // in CLI mode, show column headers in table format
}

const mullvadTCD = "mullvad.ts.net."

func runStatus(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("'tailscale status' 出现了意外的非 flag 参数")
	}
	getStatus := localClient.Status
	if !statusArgs.peers {
		getStatus = localClient.StatusWithoutPeers
	}
	st, err := getStatus(ctx)
	if err != nil {
		return fixTailscaledConnectError(err)
	}
	if statusArgs.json {
		if statusArgs.active {
			for peer, ps := range st.Peer {
				if !ps.Active {
					delete(st.Peer, peer)
				}
			}
		}
		j, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return err
		}
		printf("%s", j)
		return nil
	}
	if statusArgs.web {
		ln, err := net.Listen("tcp", statusArgs.listen)
		if err != nil {
			return err
		}
		statusURL := netmon.HTTPOfListener(ln)
		printf("正在 %v 上提供 Tailscale 状态服务 ...\n", statusURL)
		go func() {
			<-ctx.Done()
			ln.Close()
		}()
		if statusArgs.browser {
			if f, ok := hookOpenURL.GetOk(); ok {
				go f(statusURL)
			}
		}
		err = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.RequestURI != "/" {
				http.NotFound(w, r)
				return
			}
			st, err := localClient.Status(ctx)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			st.WriteHTML(w)
		}))
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}

	printHealth := func() {
		printf("# 健康检查：\n")
		for _, m := range st.Health {
			printf("#     - %s\n", m)
		}
	}

	description, ok := isRunningOrStarting(st)
	if !ok {
		// print health check information if we're in a weird state, as it might
		// provide context about why we're in that weird state.
		if len(st.Health) > 0 && (st.BackendState == ipn.Starting.String() || st.BackendState == ipn.NoState.String()) {
			printHealth()
			outln()
		}
		outln(description)
		os.Exit(1)
	}

	w := tabwriter.NewWriter(Stdout, 0, 0, 2, ' ', 0)
	f := func(format string, a ...any) { fmt.Fprintf(w, format, a...) }
	if statusArgs.header {
		fmt.Fprintln(w, "IP\t主机名\t拥有者\t操作系统\t状态\t")
		fmt.Fprintln(w, "--\t--------\t-----\t--\t------\t")
	}

	printPS := func(ps *ipnstate.PeerStatus) {
		f("%s\t%s\t%s\t%s\t",
			firstIPString(ps.TailscaleIPs),
			dnsOrQuoteHostname(st, ps),
			ownerLogin(st, ps),
			ps.OS,
		)
		relay := ps.Relay
		anyTraffic := ps.TxBytes != 0 || ps.RxBytes != 0
		var offline string
		if !ps.Online {
			offline = "；离线" + lastSeenFmt(ps.LastSeen)
		}
		if !ps.Active {
			if ps.ExitNode {
				f("空闲；出口节点%s", offline)
			} else if ps.ExitNodeOption {
				f("空闲；提供出口节点%s", offline)
			} else if anyTraffic {
				f("空闲%s", offline)
			} else if !ps.Online {
				f("离线%s", lastSeenFmt(ps.LastSeen))
			} else {
				f("-")
			}
		} else {
			f("活动；")
			if ps.ExitNode {
				f("出口节点；")
			} else if ps.ExitNodeOption {
				f("提供出口节点；")
			}
			if relay != "" && ps.CurAddr == "" && ps.PeerRelay == "" {
				f("中继 %q", relay)
			} else if ps.CurAddr != "" {
				f("直连 %s", ps.CurAddr)
			} else if ps.PeerRelay != "" {
				f("对等中继 %s", ps.PeerRelay)
			}
			if !ps.Online {
				f("%s", offline)
			}
		}
		if anyTraffic {
			f("，发送 %d 接收 %d", ps.TxBytes, ps.RxBytes)
		}
		f("\t\n")
	}

	if statusArgs.self && st.Self != nil {
		printPS(st.Self)
	}

	locBasedExitNode := false
	if statusArgs.peers {
		var peers []*ipnstate.PeerStatus
		for _, peer := range st.Peers() {
			ps := st.Peer[peer]
			if ps.ShareeNode {
				continue
			}
			if ps.ExitNodeOption && !ps.ExitNode && strings.HasSuffix(ps.DNSName, mullvadTCD) {
				// Mullvad exit nodes are only shown with the `exit-node list` command.
				locBasedExitNode = true
				continue
			}
			peers = append(peers, ps)
		}
		ipnstate.SortPeers(peers)
		for _, ps := range peers {
			if statusArgs.active && !ps.Active {
				continue
			}
			printPS(ps)
		}
	}
	w.Flush()

	if locBasedExitNode {
		outln()
		printf("# 要查看包括基于位置的出口节点在内的完整出口节点列表，请运行 `tailscale exit-node list`  \n")
	}
	if len(st.Health) > 0 {
		outln()
		printHealth()
	}
	if f, ok := hookPrintFunnelStatus.GetOk(); ok {
		f(ctx)
	}
	return nil
}

var hookOpenURL feature.Hook[func(string) error]

var hookPrintFunnelStatus feature.Hook[func(context.Context)]

// isRunningOrStarting reports whether st is in state Running or Starting.
// It also returns a description of the status suitable to display to a user.
func isRunningOrStarting(st *ipnstate.Status) (description string, ok bool) {
	switch st.BackendState {
	default:
		return fmt.Sprintf("意外状态：%s", st.BackendState), false
	case ipn.Stopped.String():
		return "Tailscale 已停止。", false
	case ipn.NeedsLogin.String():
		s := "已登出。"
		if st.AuthURL != "" {
			s += fmt.Sprintf("\n在此登录：%s", st.AuthURL)
		}
		return s, false
	case ipn.NeedsMachineAuth.String():
		return "主机尚未被 tailnet 管理员批准。", false
	case ipn.Running.String(), ipn.Starting.String():
		return st.BackendState, true
	}
}

func dnsOrQuoteHostname(st *ipnstate.Status, ps *ipnstate.PeerStatus) string {
	baseName := dnsname.TrimSuffix(ps.DNSName, st.MagicDNSSuffix)
	if baseName != "" {
		if strings.HasPrefix(baseName, "xn-") {
			if u, err := idna.ToUnicode(baseName); err == nil {
				return fmt.Sprintf("%s (%s)", baseName, u)
			}
		}
		return baseName
	}
	return fmt.Sprintf("(%q)", dnsname.SanitizeHostname(ps.HostName))
}

func ownerLogin(st *ipnstate.Status, ps *ipnstate.PeerStatus) string {
	// We prioritize showing the name of the sharer as the owner of a node if
	// it's different from the node's user. This is less surprising: if user B
	// from a company shares user's C node from the same company with user A who
	// don't know user C, user A might be surprised to see user C listed in
	// their netmap. We've historically (2021-01..2023-08) always shown the
	// sharer's name in the UI. Perhaps we want to show both here? But the CLI's
	// a bit space constrained.
	uid := cmp.Or(ps.AltSharerUserID, ps.UserID)
	if uid.IsZero() {
		return "-"
	}
	u, ok := st.User[uid]
	if !ok {
		return fmt.Sprint(uid)
	}
	if i := strings.Index(u.LoginName, "@"); i != -1 {
		return u.LoginName[:i+1]
	}
	return u.LoginName
}

func firstIPString(v []netip.Addr) string {
	if len(v) == 0 {
		return ""
	}
	return v[0].String()
}
