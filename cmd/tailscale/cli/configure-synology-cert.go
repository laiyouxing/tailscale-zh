// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux && !ts_omit_acme && !ts_omit_synology

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"runtime"
	"slices"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/hostinfo"
	"tailscale.com/ipn"
	"tailscale.com/version/distro"
)

func init() {
	maybeConfigSynologyCertCmd = synologyConfigureCertCmd
}

func synologyConfigureCertCmd() *ffcli.Command {
	if runtime.GOOS != "linux" || distro.Get() != distro.Synology {
		return nil
	}
	return &ffcli.Command{
		Name:       "synology-cert",
		Exec:       runConfigureSynologyCert,
		ShortHelp:  "为你的 tailnet 配置带 TLS 证书的 Synology",
		ShortUsage: "synology-cert [--domain <domain>]",
		LongHelp: strings.TrimSpace(`
此命令用于在 Synology 设备上以 root 身份定期运行，以创建或刷新
tailnet 域的 TLS 证书。

参见：https://tailscale.com/kb/1153/enabling-https
`),
		FlagSet: (func() *flag.FlagSet {
			fs := newFlagSet("synology-cert")
			fs.StringVar(&synologyConfigureCertArgs.domain, "domain", "", "要创建或刷新证书的 tailnet 域。若仅存在一个域，则忽略此项。")
			return fs
		})(),
	}
}

var synologyConfigureCertArgs struct {
	domain string
}

func runConfigureSynologyCert(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("未知参数")
	}
	if runtime.GOOS != "linux" || distro.Get() != distro.Synology {
		return errors.New("仅在 Synology 上实现")
	}
	if uid := os.Getuid(); uid != 0 {
		return fmt.Errorf("必须以 root 身份运行，而非 %q（%v）", os.Getenv("USER"), uid)
	}
	hi := hostinfo.New()
	isDSM6 := strings.HasPrefix(hi.DistroVersion, "6.")
	isDSM7 := strings.HasPrefix(hi.DistroVersion, "7.")
	if !isDSM6 && !isDSM7 {
		return fmt.Errorf("不支持的 DSM 版本 %q", hi.DistroVersion)
	}

	domain := synologyConfigureCertArgs.domain
	if st, err := localClient.Status(ctx); err == nil {
		if st.BackendState != ipn.Running.String() {
			return fmt.Errorf("Tailscale 未运行。")
		} else if len(st.CertDomains) == 0 {
			return fmt.Errorf("你的 tailnet 未启用/配置 TLS 证书支持。")
		} else if len(st.CertDomains) == 1 {
			if domain != "" && domain != st.CertDomains[0] {
				log.Printf("忽略提供的域 %q，将为其创建 TLS 证书 %q。\n", domain, st.CertDomains[0])
			}
			domain = st.CertDomains[0]
		} else {
			var found bool
			if slices.Contains(st.CertDomains, domain) {
				found = true
			}
			if !found {
				return fmt.Errorf("域 %q 不是有效的域选项之一：%q。", domain, st.CertDomains)
			}
		}
	}

	// Check for an existing certificate, and replace it if it already exists
	var id string
	certs, err := listCerts(ctx, synowebapiCommand{})
	if err != nil {
		return err
	}
	for _, c := range certs {
		if c.Subject.CommonName == domain {
			id = c.ID
			break
		}
	}

	certPEM, keyPEM, err := localClient.CertPair(ctx, domain)
	if err != nil {
		return err
	}

	// Certs have to be written to file for the upload command to work.
	tmpDir, err := os.MkdirTemp("", "")
	if err != nil {
		return fmt.Errorf("无法创建临时目录：%w", err)
	}
	defer os.RemoveAll(tmpDir)
	keyFile := path.Join(tmpDir, "key.pem")
	os.WriteFile(keyFile, keyPEM, 0600)
	certFile := path.Join(tmpDir, "cert.pem")
	os.WriteFile(certFile, certPEM, 0600)

	if err := uploadCert(ctx, synowebapiCommand{}, certFile, keyFile, id); err != nil {
		return err
	}

	return nil
}

type subject struct {
	CommonName string `json:"common_name"`
}

type certificateInfo struct {
	ID      string  `json:"id"`
	Desc    string  `json:"desc"`
	Subject subject `json:"subject"`
}

// listCerts fetches a list of the certificates that DSM knows about
func listCerts(ctx context.Context, c synoAPICaller) ([]certificateInfo, error) {
	rawData, err := c.Call(ctx, "SYNO.Core.Certificate.CRT", "list", nil)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Certificates []certificateInfo `json:"certificates"`
	}
	if err := json.Unmarshal(rawData, &payload); err != nil {
		return nil, fmt.Errorf("解码证书列表响应负载：%w", err)
	}

	return payload.Certificates, nil
}

// uploadCert creates or replaces a certificate. If id is given, it will attempt to replace the certificate with that ID.
func uploadCert(ctx context.Context, c synoAPICaller, certFile, keyFile string, id string) error {
	params := map[string]string{
		"key_tmp":  keyFile,
		"cert_tmp": certFile,
		"desc":     "Tailnet 证书",
	}
	if id != "" {
		params["id"] = id
	}

	rawData, err := c.Call(ctx, "SYNO.Core.Certificate", "import", params)
	if err != nil {
		return err
	}

	var payload struct {
		NewID string `json:"id"`
	}
	if err := json.Unmarshal(rawData, &payload); err != nil {
		return fmt.Errorf("解码证书上传响应负载：%w", err)
	}
	log.Printf("Tailnet 证书已上传，ID %q。", payload.NewID)

	return nil

}

type synoAPICaller interface {
	Call(context.Context, string, string, map[string]string) (json.RawMessage, error)
}

type apiResponse struct {
	Success bool            `json:"success"`
	Error   *apiError       `json:"error,omitempty"`
	Data    json.RawMessage `json:"data"`
}

type apiError struct {
	Code   int64  `json:"code"`
	Errors string `json:"errors"`
}

// synowebapiCommand implements synoAPICaller using the /usr/syno/bin/synowebapi binary. Must be run as root.
type synowebapiCommand struct{}

func (s synowebapiCommand) Call(ctx context.Context, api, method string, params map[string]string) (json.RawMessage, error) {
	args := []string{"--exec", fmt.Sprintf("api=%s", api), fmt.Sprintf("method=%s", method)}

	for k, v := range params {
		args = append(args, fmt.Sprintf("%s=%q", k, v))
	}

	out, err := exec.CommandContext(ctx, "/usr/syno/bin/synowebapi", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("调用 %q API 的 %q 方法：%v, %s", method, api, err, out)
	}

	var payload apiResponse
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("解码 %q API 的 %q 方法的响应 JSON：%w", method, api, err)
	}

	if payload.Error != nil {
		return nil, fmt.Errorf("%q API 的 %q 方法返回了错误响应：%v", method, api, payload.Error)
	}

	return payload.Data, nil
}
