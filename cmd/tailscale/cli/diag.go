// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build (linux || windows || darwin) && !ts_omit_cliconndiag

package cli

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	ps "github.com/mitchellh/go-ps"
	"tailscale.com/version/distro"
)

func init() {
	hookFixTailscaledConnectError.Set(fixTailscaledConnectErrorImpl)
}

// fixTailscaledConnectErrorImpl is called when the local tailscaled has
// been determined unreachable due to the provided origErr value. It
// returns either the same error or a better one to help the user
// understand why tailscaled isn't running for their platform.
func fixTailscaledConnectErrorImpl(origErr error) error {
	procs, err := ps.Processes()
	if err != nil {
		return fmt.Errorf("无法连接到本地 tailscaled 进程，且在查找它时也无法枚举进程")
	}
	var foundProc ps.Process
	for _, proc := range procs {
		base := filepath.Base(proc.Executable())
		if base == "tailscaled" {
			foundProc = proc
			break
		}
		if runtime.GOOS == "darwin" && base == "IPNExtension" {
			foundProc = proc
			break
		}
		if runtime.GOOS == "windows" && strings.EqualFold(base, "tailscaled.exe") {
			foundProc = proc
			break
		}
	}
	if foundProc == nil {
		switch runtime.GOOS {
		case "windows":
			return fmt.Errorf("无法连接到本地 tailscaled 进程；Tailscale 服务是否正在运行？")
		case "darwin":
			return fmt.Errorf("无法连接到本地 Tailscale 服务；Tailscale 是否正在运行？")
		case "linux":
			var hint string
			if isSystemdSystem() {
				hint = "（sudo systemctl start tailscaled ?）"
			}
			return fmt.Errorf("无法连接到本地 tailscaled；它似乎没有在运行%s", hint)
		}
		return fmt.Errorf("无法连接到本地 tailscaled 进程；它似乎没有在运行")
	}
	return fmt.Errorf("无法连接到本地 tailscaled（它似乎以 %v 运行，pid %v）。错误：%w", foundProc.Executable(), foundProc.Pid(), origErr)
}

// isSystemdSystem reports whether the current machine uses systemd
// and in particular whether the systemctl command is available.
func isSystemdSystem() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	switch distro.Get() {
	case distro.QNAP, distro.Gokrazy, distro.Synology, distro.Unraid:
		return false
	}
	_, err := exec.LookPath("systemctl")
	return err == nil
}
