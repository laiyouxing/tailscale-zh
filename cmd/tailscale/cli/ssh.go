// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/envknob"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/net/tsaddr"
	"tailscale.com/paths"
	"tailscale.com/version"
)

var sshCmd = &ffcli.Command{
	Name:       "ssh",
	ShortUsage: "tailscale ssh [user@]<host> [args...]",
	ShortHelp:  "SSH 到 Tailscale 主机",
	LongHelp: strings.TrimSpace(`

'tailscale ssh' 命令是对系统 'ssh' 命令的一个可选封装，
在某些场景下很有用。Tailscale SSH 并不要求使用它；
大多数运行 Tailscale SSH 服务器的用户更愿意直接使用普通的
'ssh' 命令或他们惯用的 SSH 客户端。

'tailscale ssh' 封装额外提供了一些能力：

* 它使用 MagicDNS 解析参数中的目标服务器名称，
  即使 --accept-dns=false 也是如此。
* 它通过向系统 'ssh' 命令提供一条 ProxyCommand，
  经由 tailscaled 的管道连接，从而在用户态网络模式下工作。
* 它会自动将目标服务器的 SSH 主机密钥与节点通过
  Tailscale 协调服务器通告的 SSH 主机密钥进行比对。
`),
	Exec: runSSH,
}

func runSSH(ctx context.Context, args []string) error {
	if runtime.GOOS == "darwin" && version.IsMacAppStore() && !envknob.UseWIPCode() {
		return errors.New("'tailscale ssh' 子命令在通过 App Store 或 TestFlight 分发的 macOS 版本中不可用。\n请安装 Tailscale 的独立版本（从 https://pkgs.tailscale.com 下载），或者改用普通的 'ssh' 客户端。")
	}
	if len(args) == 0 {
		return errors.New("用法：tailscale ssh [用户@]<主机>")
	}
	arg, argRest := args[0], args[1:]
	username, host, ok := strings.Cut(arg, "@")
	if !ok {
		host = arg
		username = ""
	}

	st, err := localClient.Status(ctx)
	if err != nil {
		return err
	}

	prefs, err := localClient.GetPrefs(ctx)
	if err != nil {
		return err
	}

	// hostForSSH is the hostname we'll tell OpenSSH we're
	// connecting to, so we have to maintain fewer entries in the
	// known_hosts files.
	hostForSSH := host
	ps, ok := peerStatusFromArg(st, host)
	if ok {
		hostForSSH = ps.DNSName

		// If MagicDNS isn't enabled on the client,
		// we will use the first IPv4 we know about
		// or fallback to the first IPv6 address
		if !prefs.CorpDNS {
			ipHost, found := ipFromPeerStatus(ps)
			if found {
				hostForSSH = ipHost
			}
		}
	}

	ssh, err := findSSH()
	if err != nil {
		// TODO(bradfitz): use Go's crypto/ssh client instead
		// of failing. But for now:
		return fmt.Errorf("未找到系统的 'ssh' 命令：%w", err)
	}
	knownHostsFile, err := writeKnownHosts(st)
	if err != nil {
		return err
	}

	argv := []string{ssh}

	if envknob.Bool("TS_DEBUG_SSH_EXEC") {
		argv = append(argv, "-vvv")
	}
	argv = append(argv,
		// Only trust SSH hosts that we know about.
		"-o", fmt.Sprintf("UserKnownHostsFile %q", knownHostsFile),
		"-o", "UpdateHostKeys no",
		"-o", "StrictHostKeyChecking yes",
		"-o", "CanonicalizeHostname no", // https://github.com/tailscale/tailscale/issues/10348
	)

	// MagicDNS is usually working on macOS anyway and they're not in userspace
	// mode, so 'nc' isn't very useful.
	if runtime.GOOS != "darwin" {
		socketArg := ""
		if localClient.Socket != "" && localClient.Socket != paths.DefaultTailscaledSocket() {
			socketArg = fmt.Sprintf("--socket=%q", localClient.Socket)
		}

		argv = append(argv,
			"-o", fmt.Sprintf("ProxyCommand %q %s nc %%h %%p",
				// os.Executable() would return the real running binary but in case tailscale is built with the ts_include_cli tag,
				// we need to return the started symlink instead
				os.Args[0],
				socketArg,
			))
	}

	// Explicitly rebuild the user@host argument rather than
	// passing it through.  In general, the use of OpenSSH's ssh
	// binary is a crutch for now.  We don't want to be
	// Hyrum-locked into passing through all OpenSSH flags to the
	// OpenSSH client forever. We try to make our flags and args
	// be compatible, but only a subset. The "tailscale ssh"
	// command should be a simple and portable one. If they want
	// to use a different one, we'll later be making stock ssh
	// work well by default too. (doing things like automatically
	// setting known_hosts, etc)
	if username == "" {
		argv = append(argv, hostForSSH)
	} else {
		argv = append(argv, username+"@"+hostForSSH)
	}

	argv = append(argv, argRest...)

	if envknob.Bool("TS_DEBUG_SSH_EXEC") {
		log.Printf("Running: %q, %q ...", ssh, argv)
	}

	return execSSH(ssh, argv)
}

func writeKnownHosts(st *ipnstate.Status) (knownHostsFile string, err error) {
	confDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	tsConfDir := filepath.Join(confDir, "tailscale")
	if err := os.MkdirAll(tsConfDir, 0700); err != nil {
		return "", err
	}
	knownHostsFile = filepath.Join(tsConfDir, "ssh_known_hosts")
	want := genKnownHosts(st)
	if cur, err := os.ReadFile(knownHostsFile); err != nil || !bytes.Equal(cur, want) {
		if err := os.WriteFile(knownHostsFile, want, 0644); err != nil {
			return "", err
		}
	}
	return knownHostsFile, nil
}

func genKnownHosts(st *ipnstate.Status) []byte {
	var buf bytes.Buffer
	for _, k := range st.Peers() {
		ps := st.Peer[k]
		for _, hk := range ps.SSH_HostKeys {
			hostKey := strings.TrimSpace(hk)
			if strings.ContainsAny(hostKey, "\n\r") { // invalid
				continue
			}
			fmt.Fprintf(&buf, "%s %s\n", ps.DNSName, hostKey)
			for _, ip := range ps.TailscaleIPs {
				fmt.Fprintf(&buf, "%s %s\n", ip.String(), hostKey)
			}
		}
	}
	return buf.Bytes()
}

// peerStatusFromArg returns the PeerStatus that matches
// the input arg which can be a base name, full DNS name, or an IP.
func peerStatusFromArg(st *ipnstate.Status, arg string) (*ipnstate.PeerStatus, bool) {
	if arg == "" {
		return nil, false
	}
	argIP, _ := netip.ParseAddr(arg)
	for _, ps := range st.Peer {
		if argIP.IsValid() {
			if slices.Contains(ps.TailscaleIPs, argIP) {
				return ps, true
			}
			continue
		}
		if strings.EqualFold(strings.TrimSuffix(arg, "."), strings.TrimSuffix(ps.DNSName, ".")) {
			return ps, true
		}
		if base, _, ok := strings.Cut(ps.DNSName, "."); ok && strings.EqualFold(base, arg) {
			return ps, true
		}
	}
	return nil, false
}

// nodeDNSNameFromArg returns the PeerStatus.DNSName value from a peer
// in st that matches the input arg which can be a base name, full
// DNS name, or an IP.
func nodeDNSNameFromArg(st *ipnstate.Status, arg string) (dnsName string, ok bool) {
	if arg == "" {
		return
	}
	argIP, _ := netip.ParseAddr(arg)
	for _, ps := range st.Peer {
		dnsName = ps.DNSName
		if argIP.IsValid() {
			if slices.Contains(ps.TailscaleIPs, argIP) {
				return dnsName, true
			}
			continue
		}
		if strings.EqualFold(strings.TrimSuffix(arg, "."), strings.TrimSuffix(dnsName, ".")) {
			return dnsName, true
		}
		if base, _, ok := strings.Cut(ps.DNSName, "."); ok && strings.EqualFold(base, arg) {
			return dnsName, true
		}
	}
	return "", false
}

func ipFromPeerStatus(ps *ipnstate.PeerStatus) (string, bool) {
	if len(ps.TailscaleIPs) < 1 {
		return "", false
	}

	// Look for a IPv4 address or default to the first IP of the list
	for _, ip := range ps.TailscaleIPs {
		if ip.Is4() {
			return ip.String(), true
		}
	}
	return ps.TailscaleIPs[0].String(), true
}

// getSSHClientEnvVar returns the "SSH_CLIENT" environment variable
// for the current process group, if any.
var getSSHClientEnvVar = func() string {
	return ""
}

// isSSHOverTailscale checks if the invocation is in a SSH session over Tailscale.
// It is used to detect if the user is about to take an action that might result in them
// disconnecting from the machine (e.g. disabling SSH)
func isSSHOverTailscale() bool {
	sshClient := getSSHClientEnvVar()
	if sshClient == "" {
		return false
	}
	ipStr, _, ok := strings.Cut(sshClient, " ")
	if !ok {
		return false
	}
	ip, err := netip.ParseAddr(ipStr)
	if err != nil {
		return false
	}
	return tsaddr.IsTailscaleIP(ip)
}
