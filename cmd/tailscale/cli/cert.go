// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !js && !ts_omit_acme

package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v3/ffcli"
	"software.sslmate.com/src/go-pkcs12"
	"tailscale.com/atomicfile"
	"tailscale.com/feature/buildfeatures"
	"tailscale.com/health"
	"tailscale.com/ipn"
	"tailscale.com/tsconst"
	"tailscale.com/version"
)

func init() {
	maybeCertCmd = func() *ffcli.Command {
		return &ffcli.Command{
			Name:       "cert",
			Exec:       runCert,
			ShortHelp:  "获取 TLS 证书",
			ShortUsage: "tailscale cert [flags] <domain>",
			FlagSet: (func() *flag.FlagSet {
				fs := newFlagSet("cert")
				fs.StringVar(&certArgs.certFile, "cert-file", "", "证书输出文件，或用 \"-\" 表示输出到标准输出；若 --cert-file 与 --key-file 均未设置，则默认为 DOMAIN.crt")
				fs.StringVar(&certArgs.keyFile, "key-file", "", "私钥输出文件，或用 \"-\" 表示输出到标准输出；若 --cert-file 与 --key-file 均未设置，则默认为 DOMAIN.key")
				fs.BoolVar(&certArgs.serve, "serve-demo", false, "若为 true，则使用证书在 :443 端口提供演示服务，而不是将文件写入磁盘")
				fs.DurationVar(&certArgs.minValidity, "min-validity", 0, "确保证书至少在此时间段内有效；若未设置该标志或设为 0，则输出的证书不会过期，但有效期可能会变化；允许的最大 min-validity 取决于 CA")
				return fs
			})(),
		}
	}
}

var certArgs struct {
	certFile    string
	keyFile     string
	serve       bool
	minValidity time.Duration
}

func runCert(ctx context.Context, args []string) error {
	if certArgs.serve {
		s := &http.Server{
			Addr: ":443",
			TLSConfig: &tls.Config{
				GetCertificate: localClient.GetCertificate,
			},
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.TLS != nil && !strings.Contains(r.Host, ".") && r.Method == "GET" {
					if v, ok := localClient.ExpandSNIName(r.Context(), r.Host); ok {
						http.Redirect(w, r, "https://"+v+r.URL.Path, http.StatusTemporaryRedirect)
						return
					}
				}
				fmt.Fprintf(w, "<h1>Hello from Tailscale</h1>It works.")
			}),
		}
		switch len(args) {
		case 0:
			// Nothing.
		case 1:
			s.Addr = args[0]
		default:
			return errors.New("参数过多；使用 --serve-demo（监听地址）时最多允许 1 个参数")
		}

		log.Printf("正在 %s 上运行 TLS 服务器 ...", s.Addr)
		return s.ListenAndServeTLS("", "")
	}

	if len(args) != 1 {
		var hint bytes.Buffer
		if st, err := localClient.Status(ctx); err == nil {
				if st.BackendState != ipn.Running.String() {
				fmt.Fprintf(&hint, "\nTailscale 未运行。\n")
			} else if len(st.CertDomains) == 0 {
				fmt.Fprintf(&hint, "\n您的 tailnet 未启用/未配置 HTTPS 证书支持。\n")
			} else if len(st.CertDomains) == 1 {
				fmt.Fprintf(&hint, "\n域名请使用 %q。\n", st.CertDomains[0])
			} else {
				fmt.Fprintf(&hint, "\n有效的域名选项：%q。\n", st.CertDomains)
			}
		}
		return fmt.Errorf("用法: tailscale cert [flags] <domain>%s", hint.Bytes())
	}
	domain := args[0]

	printf := func(format string, a ...any) {
		printf(format, a...)
	}
	if certArgs.certFile == "-" || certArgs.keyFile == "-" {
		printf = log.Printf
		log.SetFlags(0)
	}
	if certArgs.certFile == "" && certArgs.keyFile == "" {
		fileBase := strings.Replace(domain, "*.", "wildcard_.", 1)
		certArgs.certFile = fileBase + ".crt"
		certArgs.keyFile = fileBase + ".key"
	}
	if buildfeatures.HasHealth {
		watchCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		go watchCertPendingHealth(watchCtx, domain)
	}
	certPEM, keyPEM, err := localClient.CertPairWithValidity(ctx, domain, certArgs.minValidity)
	if err != nil {
		return err
	}
	needMacWarning := version.IsSandboxedMacOS()
	macWarn := func() {
		if !needMacWarning {
			return
		}
		needMacWarning = false
		dir := "io.tailscale.ipn.macos"
		if version.IsMacSysExt() {
			dir = "io.tailscale.ipn.macsys"
		}
		printf("警告：macOS CLI 运行在沙盒中；此二进制文件的文件系统写入会进入 $HOME/Library/Containers/%s/Data\n", dir)
	}
	if certArgs.certFile != "" {
		certChanged, err := writeIfChanged(certArgs.certFile, certPEM, 0644)
		if err != nil {
			return err
		}
		if certArgs.certFile != "-" {
			macWarn()
			if certChanged {
				printf("已将公钥证书写入 %v\n", certArgs.certFile)
			} else {
				printf("公钥证书未改变，仍为 %v\n", certArgs.certFile)
			}
		}
	}
	if dst := certArgs.keyFile; dst != "" {
		contents := keyPEM
		if isPKCS12(dst) {
			var err error
			contents, err = convertToPKCS12(certPEM, keyPEM)
			if err != nil {
				return err
			}
		}
		keyChanged, err := writeIfChanged(dst, contents, 0600)
		if err != nil {
			return err
		}
		if certArgs.keyFile != "-" {
			macWarn()
			if keyChanged {
				printf("已将私钥写入 %v\n", dst)
			} else {
				printf("私钥未改变，仍为 %v\n", dst)
			}
		}
	}
	return nil
}

// watchCertPendingHealth subscribes to the IPN bus and prints the
// [tsconst.HealthWarnableTLSCertPending] warning to stderr if it appears
// for domain while a cert fetch is in flight. It returns once it has
// printed the warning or ctx is done.
//
// Subscription is delayed 1 second so we don't print anything when the
// daemon returns a cached cert quickly.
func watchCertPendingHealth(ctx context.Context, domain string) {
	select {
	case <-time.After(1 * time.Second):
	case <-ctx.Done():
		return
	}
	watcher, err := localClient.WatchIPNBus(ctx, ipn.NotifyInitialHealthState|ipn.NotifyNoNetMap)
	if err != nil {
		return
	}
	defer watcher.Close()
	for {
		n, err := watcher.Next()
		if err != nil {
			return
		}
		if n.Health == nil {
			continue
		}
		ws, ok := n.Health.Warnings[tsconst.HealthWarnableTLSCertPending]
		if !ok {
			continue
		}
		if !strings.Contains(ws.Args[health.ArgDomains], domain) {
			continue
		}
		fmt.Fprintf(os.Stderr, "%s: %s\n", ws.Title, ws.Text)
		return
	}
}

func writeIfChanged(filename string, contents []byte, mode os.FileMode) (changed bool, err error) {
	if filename == "-" {
		Stdout.Write(contents)
		return false, nil
	}
	if old, err := os.ReadFile(filename); err == nil && bytes.Equal(contents, old) {
		return false, nil
	}
	if err := atomicfile.WriteFile(filename, contents, mode); err != nil {
		return false, err
	}
	return true, nil
}

func isPKCS12(dst string) bool {
	return strings.HasSuffix(dst, ".p12") || strings.HasSuffix(dst, ".pfx")
}

func convertToPKCS12(certPEM, keyPEM []byte) ([]byte, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	var certs []*x509.Certificate
	for _, c := range cert.Certificate {
		cert, err := x509.ParseCertificate(c)
		if err != nil {
			return nil, err
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		return nil, errors.New("无证书")
	}
	// TODO(bradfitz): I'm not sure this is right yet. The goal was to make this
	// work for https://github.com/tailscale/tailscale/issues/2928 but I'm still
	// fighting Windows.
	return pkcs12.Encode(rand.Reader, cert.PrivateKey, certs[0], certs[1:], "" /* no password */)
}
