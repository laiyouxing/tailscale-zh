// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_webclient

package cli

import (
	"cmp"
	"context"
	"crypto/tls"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/cgi"
	"net/netip"
	"os"
	"os/signal"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/client/web"
	"tailscale.com/ipn"
	"tailscale.com/tsconst"
)

func init() {
	maybeWebCmd = webCmd
}

func webCmd() *ffcli.Command {
	return &ffcli.Command{
		Name:       "web",
		ShortUsage: "tailscale web [flags]",
		ShortHelp:  "运行一个用于控制 Tailscale 的 Web 服务器",

		LongHelp: strings.TrimSpace(`
"tailscale web" 运行一个用于控制 Tailscale 守护进程的 Web 服务器。

它主要面向 Synology、QNAP 及其他 NAS 设备，这类设备上 Web 界面
是控制 Tailscale 的自然入口，而非 CLI 或原生应用。
`),

		FlagSet: (func() *flag.FlagSet {
			webf := newFlagSet("web")
			webf.StringVar(&webArgs.listen, "listen", "localhost:8088", "监听地址；使用端口 0 表示自动分配")
			webf.BoolVar(&webArgs.cgi, "cgi", false, "作为 CGI 脚本运行")
			webf.StringVar(&webArgs.prefix, "prefix", "", "添加到请求的 URL 前缀（用于 cgi 或反向代理）")
			webf.BoolVar(&webArgs.readonly, "readonly", false, "以只读模式运行 Web 界面")
			webf.StringVar(&webArgs.origin, "origin", "", "提供 Web 界面的来源（origin）（若位于反向代理之后或与 cgi 配合使用时）")
			return webf
		})(),
		Exec: runWeb,
	}
}

var webArgs struct {
	listen   string
	cgi      bool
	prefix   string
	readonly bool
	origin   string
}

func tlsConfigFromEnvironment() *tls.Config {
	crt := os.Getenv("TLS_CRT_PEM")
	key := os.Getenv("TLS_KEY_PEM")
	if crt == "" || key == "" {
		return nil
	}

	// We support passing in the complete certificate and key from environment
	// variables because pfSense stores its cert+key in the PHP config. We populate
	// TLS_CRT_PEM and TLS_KEY_PEM from PHP code before starting tailscale web.
	// These are the PEM-encoded Certificate and Private Key.

	cert, err := tls.X509KeyPair([]byte(crt), []byte(key))
	if err != nil {
		log.Printf("tlsConfigFromEnvironment: %v", err)

		// Fallback to unencrypted HTTP.
		return nil
	}

	return &tls.Config{Certificates: []tls.Certificate{cert}}
}

func runWeb(ctx context.Context, args []string) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	if len(args) > 0 {
		return fmt.Errorf("过多的非标志参数：%q", args)
	}

	var selfIP netip.Addr
	st, err := localClient.StatusWithoutPeers(ctx)
	if err == nil && st.Self != nil && len(st.Self.TailscaleIPs) > 0 {
		selfIP = st.Self.TailscaleIPs[0]
	}

	var existingWebClient bool
	if prefs, err := localClient.GetPrefs(ctx); err == nil {
		existingWebClient = prefs.RunWebClient
	}
	var startedManagementClient bool // we started the management client
	if !existingWebClient && !webArgs.readonly {
		// Also start full client in tailscaled.
		log.Printf("starting tailscaled web client at http://%s\n", netip.AddrPortFrom(selfIP, tsconst.WebListenPort))
		if err := setRunWebClient(ctx, true); err != nil {
			return fmt.Errorf("starting web client in tailscaled: %w", err)
		}
		startedManagementClient = true
	}

	opts := web.ServerOpts{
		Mode:        web.LoginServerMode,
		CGIMode:     webArgs.cgi,
		PathPrefix:  webArgs.prefix,
		LocalClient: &localClient,
	}
	if webArgs.readonly {
		opts.Mode = web.ReadOnlyServerMode
	}
	if webArgs.origin != "" {
		opts.OriginOverride = webArgs.origin
	}
	webServer, err := web.NewServer(opts)
	if err != nil {
		log.Printf("tailscale.web: %v", err)
		return err
	}
	go func() {
		select {
		case <-ctx.Done():
			// Shutdown the server.
			webServer.Shutdown()
			if !webArgs.cgi && startedManagementClient {
				log.Println("stopping tailscaled web client")
				// When not in cgi mode, shut down the tailscaled
				// web client on cli termination if we started it.
				if err := setRunWebClient(context.Background(), false); err != nil {
					log.Printf("stopping tailscaled web client: %v", err)
				}
			}
		}
		os.Exit(0)
	}()

	if webArgs.cgi {
		if err := cgi.Serve(webServer); err != nil {
			log.Printf("tailscale.cgi: %v", err)
		}
		return nil
	} else if tlsConfig := tlsConfigFromEnvironment(); tlsConfig != nil {
		server := &http.Server{
			Addr:      webArgs.listen,
			TLSConfig: tlsConfig,
			Handler:   webServer,
		}
		defer server.Shutdown(ctx)
		log.Printf("web server running on: https://%s", server.Addr)
		return server.ListenAndServeTLS("", "")
	} else {
		log.Printf("web server running on: %s", urlOfListenAddr(webArgs.listen))
		return http.ListenAndServe(webArgs.listen, webServer)
	}
}

func setRunWebClient(ctx context.Context, val bool) error {
	_, err := localClient.EditPrefs(ctx, &ipn.MaskedPrefs{
		Prefs:           ipn.Prefs{RunWebClient: val},
		RunWebClientSet: true,
	})
	return err
}

// urlOfListenAddr parses a given listen address into a formatted URL
func urlOfListenAddr(addr string) string {
	host, port, _ := net.SplitHostPort(addr)
	return fmt.Sprintf("http://%s", net.JoinHostPort(cmp.Or(host, "127.0.0.1"), port))
}
