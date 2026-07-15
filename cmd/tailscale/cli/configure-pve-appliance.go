// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_flashappliance

package cli

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/clientupdate"
	"tailscale.com/gokrazy/mkfs"
	"tailscale.com/util/prompt"
)

var pveApplianceArgs struct {
	vmid                 int
	name                 string
	storage              string
	diskSize             string
	cores                int
	memory               int
	bridge               string
	variant              string
	track                string
	gaf                  string
	addSSHAuthorizedKeys string
	start                bool
	yes                  bool
}

func pveApplianceCmd() *ffcli.Command {
	return &ffcli.Command{
		Name:       "pve-appliance",
		ShortUsage: "tailscale configure pve-appliance --storage=<name> [flags]",
		ShortHelp:  "创建一个运行 Tailscale 设备镜像的 Proxmox VE 虚拟机 [实验性]",
		LongHelp: hidden + strings.TrimSpace(`
此实验性命令从 pkgs.tailscale.com 下载一个已签名的 Tailscale 设备 GAF，
在 /var/tmp 中构建一个原始磁盘镜像，然后在本地 Proxmox VE 主机上调用
'qm create' / 'qm disk import' / 'qm set' 来创建一个由该镜像支撑的新虚拟机。
它必须在 PVE 主机本身上运行（即 'qm' 命令行可用的地方）。

导入的磁盘以 scsi0 形式挂载在 virtio-scsi-single 控制器上并启用 iothread，
网络连接到指定的网桥（默认为 vmbr0），并启用了客户机代理——配合设备内置的
qemu-guest-kragent，PVE 即可看到客户机的 IP。

该虚拟机会创建一个 virtio-serial 控制台（--serial0 socket）。一旦
虚拟机运行，在 PVE 主机上执行 'qm terminal <vmid>'，然后按
Enter，即可无需 SSH 密钥进入设备内部的 busybox shell。

已预先选取默认值，因此像下面这样不带参数的调用：

    tailscale configure pve-appliance --storage=local-lvm

就足以生成一个可启动的 Tailscale 设备虚拟机。
`),
		FlagSet: (func() *flag.FlagSet {
			fs := newFlagSet("pve-appliance")
			fs.IntVar(&pveApplianceArgs.vmid, "vmid", 0, "目标虚拟机 ID；为 0 时向 Proxmox 申请下一个可用 ID")
			fs.StringVar(&pveApplianceArgs.name, "name", "", "虚拟机名称；默认为 \"tsapp-<vmid>\"")
			fs.StringVar(&pveApplianceArgs.storage, "storage", "", "要导入磁盘的 PVE 存储（如 local-lvm、ssd2）；必填")
			fs.StringVar(&pveApplianceArgs.diskSize, "disk-size", "4G", "原始镜像大小（接受 K/M/G 后缀，如 4G、8192M）")
			fs.IntVar(&pveApplianceArgs.cores, "cores", 2, "vCPU 核心数")
			fs.IntVar(&pveApplianceArgs.memory, "memory", 1024, "内存大小（单位 MiB）")
			fs.StringVar(&pveApplianceArgs.bridge, "bridge", "vmbr0", "virtio net0 要接入的网络网桥")
			fs.StringVar(&pveApplianceArgs.variant, "variant", "vm-amd64", "设备变体：\"vm-amd64\" 或 \"vm-arm64\"")
			fs.StringVar(&pveApplianceArgs.track, "track", "", "要下载的发布通道；默认为 \""+clientupdate.CurrentTrack+"\"")
			fs.StringVar(&pveApplianceArgs.gaf, "gaf", "", "使用本地 GAF 文件而非下载（跳过签名校验）")
			fs.StringVar(&pveApplianceArgs.addSSHAuthorizedKeys, "add-ssh-authorized-keys", "", "包含到设备中以用于应急 SSH 访问的 authorized_keys 文件路径")
			fs.BoolVar(&pveApplianceArgs.start, "start", true, "导入后启动虚拟机")
			fs.BoolVar(&pveApplianceArgs.yes, "yes", false, "跳过确认提示")
			return fs
		})(),
		Exec: runPVEAppliance,
	}
}

func runPVEAppliance(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("未知参数")
	}
	if runtime.GOOS != "linux" {
		return errors.New("pve-appliance 子命令仅在 Linux 上可用；请用于 Proxmox PVE 主机")
	}
	if fi, err := os.Stat("/etc/pve"); err != nil || !fi.IsDir() {
		return errors.New("/etc/pve 不是目录：请在 Proxmox VE 主机上运行此命令")
	}
	if _, err := exec.LookPath("qm"); err != nil {
		return errors.New("在 $PATH 中未找到 `qm`：请在 Proxmox VE 主机上运行此命令")
	}
	if pveApplianceArgs.storage == "" {
		return errors.New("--storage 为必填项（例如 --storage=local-lvm）")
	}

	diskBytes, err := parseSizeBytes(pveApplianceArgs.diskSize)
	if err != nil {
		return fmt.Errorf("解析 --disk-size：%w", err)
	}

	vmid := pveApplianceArgs.vmid
	if vmid == 0 {
		vmid, err = pveNextID(ctx)
		if err != nil {
			return fmt.Errorf("获取下一个 VMID：%w", err)
		}
	}
	name := pveApplianceArgs.name
	if name == "" {
		name = fmt.Sprintf("tsapp-%d", vmid)
	}

	gafPath, gafLabel, variant, cleanup, err := obtainGAF(ctx, gafDownloadArgs{
		localGAF: pveApplianceArgs.gaf,
		track:    pveApplianceArgs.track,
		variant:  pveApplianceArgs.variant,
	})
	if err != nil {
		return err
	}
	defer cleanup()

	if !pveApplianceArgs.yes {
		printf("即将在存储 %q 上基于 %s 创建 Proxmox 虚拟机 %d（%q）\n",
			vmid, name, pveApplianceArgs.storage, gafLabel)
		printf("  cores=%d memory=%dMiB bridge=%s disk=%s start=%v\n",
			pveApplianceArgs.cores, pveApplianceArgs.memory,
			pveApplianceArgs.bridge, pveApplianceArgs.diskSize, pveApplianceArgs.start)
		if !prompt.YesNo("是否继续？", false) {
			return errors.New("已中止")
		}
	}

	imgPath, err := buildPVERawImage(gafPath, diskBytes, variant)
	if err != nil {
		return err
	}
	defer os.Remove(imgPath)

	if err := createPVEVM(ctx, vmid, name); err != nil {
		return fmt.Errorf("qm create：%w", err)
	}
	diskRef, err := importPVEDisk(ctx, vmid, pveApplianceArgs.storage, imgPath)
	if err != nil {
		return fmt.Errorf("qm disk import：%w", err)
	}
	if err := attachPVEDisk(ctx, vmid, diskRef); err != nil {
		return fmt.Errorf("qm set：%w", err)
	}

	if pveApplianceArgs.start {
		if err := runQM(ctx, "start", strconv.Itoa(vmid)); err != nil {
			return fmt.Errorf("qm start：%w", err)
		}
		printf("虚拟机 %d 已启动。\n", vmid)
	} else {
		printf("虚拟机 %d 已创建；未启动（传入 --start 可自动启动）。\n", vmid)
	}
	return nil
}

// buildPVERawImage creates a sparse raw disk image in /var/tmp and
// writes the GAF's boot + root images to it plus a fresh /perm ext4
// filesystem. The returned path is the caller's to remove.
func buildPVERawImage(gafPath string, devsize int64, variant string) (string, error) {
	zr, err := zip.OpenReader(gafPath)
	if err != nil {
		return "", fmt.Errorf("打开 GAF：%w", err)
	}
	defer zr.Close()

	bootCode, err := readGAFMember(zr.File, "mbr.img", 1<<20)
	if err != nil {
		return "", err
	}

	tmp, err := os.CreateTemp("/var/tmp", "tsapp-pve-*.raw")
	if err != nil {
		return "", err
	}
	imgPath := tmp.Name()
	if err := tmp.Truncate(devsize); err != nil {
		tmp.Close()
		os.Remove(imgPath)
		return "", fmt.Errorf("truncate：%w", err)
	}

	if err := writeApplianceImage(tmp, devsize, zr.File, bootCode, variant); err != nil {
		tmp.Close()
		os.Remove(imgPath)
		return "", err
	}

	var permFiles []mkfs.PermFile
	if k := pveApplianceArgs.addSSHAuthorizedKeys; k != "" {
		keys, err := os.ReadFile(k)
		if err != nil {
			tmp.Close()
			os.Remove(imgPath)
			return "", fmt.Errorf("读取 --add-ssh-authorized-keys：%w", err)
		}
		permFiles = append(permFiles, mkfs.PermFile{
			Path:    "breakglass.authorized_keys",
			Content: keys,
		})
		printf("正在加入用于应急访问的 SSH authorized_keys。\n")
	}
	if err := mkfs.Perm(tmp, devsize, permFiles...); err != nil {
		tmp.Close()
		os.Remove(imgPath)
		return "", fmt.Errorf("格式化 perm：%w", err)
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(imgPath)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(imgPath)
		return "", err
	}
	return imgPath, nil
}

// pveNextID asks Proxmox for the next unused VMID via pvesh.
func pveNextID(ctx context.Context) (int, error) {
	out, err := exec.CommandContext(ctx, "pvesh", "get", "/cluster/nextid").Output()
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(out))
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("解析 pvesh 输出 %q：%w", s, err)
	}
	return n, nil
}

func createPVEVM(ctx context.Context, vmid int, name string) error {
	return runQM(ctx, "create", strconv.Itoa(vmid),
		"--name", name,
		"--memory", strconv.Itoa(pveApplianceArgs.memory),
		"--cores", strconv.Itoa(pveApplianceArgs.cores),
		"--net0", "virtio,bridge="+pveApplianceArgs.bridge,
		"--scsihw", "virtio-scsi-single",
		"--serial0", "socket",
		"--agent", "1",
		"--ostype", "l26",
		"--tablet", "0",
		"--vga", "virtio",
		"--description", vmNotes(vmid),
	)
}

// vmNotes is the description shown in the PVE web UI's "Notes" panel
// for VMs we create. It tells admins the two ways to get into the
// appliance without needing an SSH key: the framebuffer console (press
// Esc for a shell) and the serial console (qm terminal).
func vmNotes(vmid int) string {
	return fmt.Sprintf(`# Tailscale 设备 [实验性]

此虚拟机的管理访问方式（无需 SSH 密钥）：

- **帧缓冲 / NoVNC 控制台**：在注册界面按 **Esc**
  即可进入 busybox shell。输入 `+"`exit`"+` 可返回
  Tailscale 状态显示。

- **串口控制台**：在此 Proxmox 主机上运行

      qm terminal %d

  然后按 **Enter** 进入 busybox shell。按 **Ctrl+O** 可
  脱离 `+"`qm terminal`"+`（保留客户机 shell 运行）。

在任一 shell 中，照常运行 `+"`tailscale`"+` 命令
（`+"`tailscale up`"+`, `+"`tailscale status`"+` 等）。
`, vmid)
}

// importPVEDisk imports src into storage on vmid and returns the
// Proxmox volume reference of the newly-imported disk (e.g.
// "local-lvm:vm-102-disk-0").
//
// The stdout of "qm disk import" isn't a stable API across Proxmox
// versions — historically it has printed variants like
// "vm-<vmid>-disk-<N>" and "importing disk … as <volid>", and the
// allocated volume name isn't always vm-<vmid>-disk-0 (e.g. if a
// stale volume with that name exists on the storage from a prior
// VM). We instead diff the VM config before and after the import
// and pick out the newly-added unusedN entry, which holds the real
// volume ref.
func importPVEDisk(ctx context.Context, vmid int, storage, src string) (volume string, err error) {
	before, err := pveVMConfig(ctx, vmid)
	if err != nil {
		return "", fmt.Errorf("读取虚拟机 %d 配置：%w", vmid, err)
	}
	if err := runQM(ctx, "disk", "import", strconv.Itoa(vmid), src, storage, "--format", "raw"); err != nil {
		return "", err
	}
	after, err := pveVMConfig(ctx, vmid)
	if err != nil {
		return "", fmt.Errorf("导入后读取虚拟机 %d 配置：%w", vmid, err)
	}
	for k, v := range after {
		if !strings.HasPrefix(k, "unused") {
			continue
		}
		if before[k] == v {
			continue
		}
		return v, nil
	}
	return "", fmt.Errorf("qm disk import 未向虚拟机 %d 的配置中添加未使用的 unusedN 条目", vmid)
}

// pveVMConfig returns the current runtime config for vmid on the local
// PVE node, as a flat key → string map. We ask pvesh for JSON since
// "qm config" text output has evolved over releases.
func pveVMConfig(ctx context.Context, vmid int) (map[string]string, error) {
	node, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	out, err := exec.CommandContext(ctx, "pvesh", "get",
		fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid),
		"--output-format", "json").Output()
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("解析 pvesh JSON：%w", err)
	}
	m := make(map[string]string, len(raw))
	for k, v := range raw {
		switch v := v.(type) {
		case string:
			m[k] = v
		case float64:
			m[k] = strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			m[k] = strconv.FormatBool(v)
		}
	}
	return m, nil
}

func attachPVEDisk(ctx context.Context, vmid int, diskRef string) error {
	return runQM(ctx, "set", strconv.Itoa(vmid),
		"--scsi0", diskRef+",iothread=1",
		"--boot", "order=scsi0",
	)
}

// runQM invokes `qm` with args, streaming its output to Stderr so the
// caller can see disk-import progress and any error messages.
func runQM(ctx context.Context, args ...string) error {
	printf("$ qm %s\n", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, "qm", args...)
	cmd.Stdout = Stderr
	cmd.Stderr = Stderr
	return cmd.Run()
}

// parseSizeBytes parses a size string like "4G", "8192M", "1024K", or
// "12345" (bytes) into a byte count. Empty string returns an error.
func parseSizeBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("大小为空")
	}
	mult := int64(1)
	switch last := s[len(s)-1]; {
	case last >= '0' && last <= '9':
		// no suffix
	default:
		switch last {
		case 'K', 'k':
			mult = 1 << 10
		case 'M', 'm':
			mult = 1 << 20
		case 'G', 'g':
			mult = 1 << 30
		case 'T', 't':
			mult = 1 << 40
		default:
			return 0, fmt.Errorf("未知的大小后缀 %q", string(last))
		}
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("大小 %d 必须为正数", n)
	}
	return n * mult, nil
}
