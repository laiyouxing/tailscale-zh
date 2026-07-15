// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_taildrop

package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-isatty"
	"github.com/peterbourgon/ff/v3/ffcli"
	"golang.org/x/time/rate"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/cmd/tailscale/cli/ffcomplete"
	"tailscale.com/envknob"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/net/tsaddr"
	"tailscale.com/tailcfg"
	tsrate "tailscale.com/tstime/rate"
	"tailscale.com/util/quarantine"
	"tailscale.com/util/truncate"
	"tailscale.com/version"
)

func init() {
	fileCmd = getFileCmd
}

func getFileCmd() *ffcli.Command {
	return &ffcli.Command{
		Name:       "file",
		ShortUsage: "tailscale file <cp|get> ...",
		ShortHelp:  "发送或接收文件",
		Subcommands: []*ffcli.Command{
			fileCpCmd,
			fileGetCmd,
		},
	}
}

type countingReader struct {
	io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(buf []byte) (int, error) {
	n, err := c.Reader.Read(buf)
	c.n.Add(int64(n))
	return n, err
}

var fileCpCmd = &ffcli.Command{
	Name:       "cp",
	ShortUsage: "tailscale file cp <files...> <target>:",
	ShortHelp:  "复制文件到主机",
	Exec:       runCp,
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("cp")
		fs.StringVar(&cpArgs.name, "name", "", "使用的替代文件名，当 <file> 为 \"-\"（标准输入）时尤其有用")
		fs.BoolVar(&cpArgs.verbose, "verbose", false, "详细输出")
		fs.BoolVar(&cpArgs.targets, "targets", false, "列出可作为文件 cp 目标的主机")
		fs.DurationVar(&cpArgs.updateInterval, "update-interval", 250*time.Millisecond, "重绘进度行的频率；为 0 或负数则完全不显示进度")
		return fs
	})(),
}

var cpArgs struct {
	name           string
	verbose        bool
	targets        bool
	updateInterval time.Duration
}

func runCp(ctx context.Context, args []string) error {
	if cpArgs.targets {
		return runCpTargets(ctx, args)
	}
	if len(args) < 2 {
		return errors.New("用法：tailscale file cp <文件...> <目标>:")
	}
	files, target := args[:len(args)-1], args[len(args)-1]
	target, ok := strings.CutSuffix(target, ":")
	if !ok {
		return fmt.Errorf("'tailscale file cp' 的最后一个参数必须以冒号结尾")
	}
	hadBrackets := false
	if strings.HasPrefix(target, "[") && strings.HasSuffix(target, "]") {
		hadBrackets = true
		target = strings.TrimSuffix(strings.TrimPrefix(target, "["), "]")
	}
	if ip, err := netip.ParseAddr(target); err == nil && ip.Is6() && !hadBrackets {
		return fmt.Errorf("IPv6 字面量必须写成 [%s]", ip)
	} else if hadBrackets && (err != nil || !ip.Is6()) {
		return errors.New("目标两侧出现了意外的方括号")
	}
	ip, _, err := tailscaleIPFromArg(ctx, target)
	if err != nil {
		return err
	}

	stableID, isOffline, err := getTargetStableID(ctx, ip)
	if err != nil {
		return fmt.Errorf("无法发送到 %s：%v", target, err)
	}

	if len(files) > 1 {
		if cpArgs.name != "" {
			return errors.New("不能与多个文件同时使用 --name=")
		}
		if slices.Contains(files, "-") {
			return errors.New("在提供文件名参数时不能将 '-' 用作标准输入文件")
		}
	}

	// outFiles tracks per-name push state, populated by a goroutine subscribed
	// to the IPN bus. tailscaled's OutgoingFile.Sent is the bytes-pulled-toward-
	// peerAPI signal; it stays at 0 until the peerAPI request body is actually
	// being read, which is what we want both for the progress display and for
	// disarming the offline warning. The CLI's local-side bytes counter would
	// say "100% sent" the moment net/http buffers a small body into the local
	// unix-socket conn to tailscaled, well before the peer has heard a thing.
	type pushState struct {
		sent      atomic.Int64
		warnTimer *time.Timer // disarmed on first byte sent to peerAPI; nil after
	}
	var (
		outMu    sync.Mutex
		outFiles = map[string]*pushState{} // keyed by file name
	)

	busCtx, cancelBus := context.WithCancel(ctx)
	defer cancelBus()
	go watchOutgoingFiles(busCtx, stableID, func(name string, sent int64) {
		outMu.Lock()
		ps := outFiles[name]
		outMu.Unlock()
		if ps == nil {
			return
		}
		// Only ever advance ps.sent forward. Bus updates can arrive late
		// (after the success path below has already written contentLength
		// to ps.sent for an instant final-100% paint), so we'd otherwise
		// regress the count and the progress printer would compute a
		// negative delta on its next tick.
		for {
			old := ps.sent.Load()
			if sent <= old {
				return
			}
			if ps.sent.CompareAndSwap(old, sent) {
				if old == 0 && ps.warnTimer != nil {
					ps.warnTimer.Stop()
				}
				return
			}
		}
	})

	for i, fileArg := range files {
		var fileContents *countingReader
		var name = cpArgs.name
		var contentLength int64 = -1
		if fileArg == "-" {
			fileContents = &countingReader{Reader: os.Stdin}
			if name == "" {
				name, fileContents, err = pickStdinFilename()
				if err != nil {
					return err
				}
			}
		} else {
			f, err := os.Open(fileArg)
			if err != nil {
				if version.IsSandboxedMacOS() {
					return errors.New("macOS 上的 Tailscale GUI 版本运行在 macOS 沙盒中，无法读取文件")
				}
				return err
			}
			defer f.Close()
			fi, err := f.Stat()
			if err != nil {
				return err
			}
			if fi.IsDir() {
				return errors.New("不支持目录")
			}
			contentLength = fi.Size()
			fileContents = &countingReader{Reader: io.LimitReader(f, contentLength)}
			if name == "" {
				name = filepath.Base(fileArg)
			}

			if envknob.Bool("TS_DEBUG_SLOW_PUSH") {
				fileContents = &countingReader{Reader: &slowReader{r: fileContents}}
			}
		}

		if cpArgs.verbose {
			log.Printf("正在发送 %q 到 %v/%v/%v ...", name, target, ip, stableID)
		}

		// Register this file with the watcher and, for the first file only,
		// arm a timer that warns the user if no bytes have flowed to peerAPI
		// after a few seconds. The watcher disarms it on first byte; PushFile
		// returning also disarms it (cleanup, below). We don't gate on the
		// netmap's Online bit (which can lag reality), but we do use it to
		// pick between two warning messages.
		ps := &pushState{}
		if i == 0 {
			ps.warnTimer = time.AfterFunc(3*time.Second, func() {
				// vtRestartLine clears whatever (possibly progress) was on
				// the current line, then we print the warning + \n so the
				// next progress redraw lands on a fresh line below.
				const vtRestartLine = "\r\x1b[K"
				if isOffline {
					fmt.Fprintf(Stderr, "%s# 警告：%s 据报处于离线状态；仍尝试发送\n", vtRestartLine, target)
				} else {
					fmt.Fprintf(Stderr, "%s# 警告：%s 没有响应；仍尝试发送\n", vtRestartLine, target)
				}
			})
		}
		outMu.Lock()
		outFiles[name] = ps
		outMu.Unlock()

		var group sync.WaitGroup
		ctxProgress, cancelProgress := context.WithCancel(ctx)
		defer cancelProgress()
		if cpArgs.updateInterval > 0 && isatty.IsTerminal(os.Stderr.Fd()) {
			group.Go(func() {
				progressPrinter(ctxProgress, name, ps.sent.Load, contentLength, cpArgs.updateInterval)
			})
		}

		err := localClient.PushFile(ctx, stableID, contentLength, name, fileContents)
		if err == nil {
			// PushFile can finish faster than the IPN bus delivers a final
			// OutgoingFile update, leaving the progress display stuck at 0%.
			// Synthesize a "fully done" count before stopping the printer so
			// its final paint shows 100%. For stdin (contentLength == -1) we
			// don't know the size, so fall back to the local read count.
			if contentLength >= 0 {
				ps.sent.Store(contentLength)
			} else {
				ps.sent.Store(fileContents.n.Load())
			}
		}
		cancelProgress()
		group.Wait() // wait for progress printer to stop before reporting the error
		if ps.warnTimer != nil {
			ps.warnTimer.Stop()
		}
		if err != nil {
			return err
		}
		if cpArgs.verbose {
			log.Printf("已发送 %q", name)
		}
	}
	return nil
}

// watchOutgoingFiles subscribes to the IPN bus and invokes onUpdate once
// per OutgoingFile event for files going to peer. It runs until ctx is
// done (which runCp does on return) and is best-effort: if the bus
// subscription fails for any reason, onUpdate simply isn't called and the
// caller's progress display stays at 0 — exactly the right degradation,
// since the warning timer will then fire on its normal 3-second deadline.
func watchOutgoingFiles(ctx context.Context, peer tailcfg.StableNodeID, onUpdate func(name string, sent int64)) {
	// NotifyPeerChanges opts in to per-peer add/remove notifications so the
	// bus stays responsive without us also subscribing to the full NetMap,
	// which we don't read here.
	w, err := localClient.WatchIPNBus(ctx, ipn.NotifyInitialOutgoingFiles|ipn.NotifyPeerChanges)
	if err != nil {
		return
	}
	defer w.Close()
	for {
		n, err := w.Next()
		if err != nil {
			return
		}
		for _, of := range n.OutgoingFiles {
			if of.PeerID != peer {
				continue
			}
			// tailscaled keeps Finished entries in its OutgoingFiles map
			// across PushFile calls (see feature/taildrop/ext.go), so a
			// re-send of the same filename will see both the old completed
			// (Sent == DeclaredSize) entry and the new in-progress one.
			// Without this filter the watcher's monotonic CAS would latch
			// onto the old entry's max value and the new transfer would
			// appear stuck at 100% from the first bus tick.
			if of.Finished {
				continue
			}
			onUpdate(of.Name, of.Sent)
		}
	}
}

// progressPrinter repaints a single-line transfer progress display every
// interval. interval must be > 0; runCp's caller gates on the
// --update-interval flag and skips invoking us when it's <= 0.
//
// It returns when ctx is done OR when it detects the transfer is stuck —
// "stuck" being: contentCount has equalled contentLength with a near-zero
// rate for >2 seconds. The stuck case prints a final newline so subsequent
// output (e.g. an error from PushFile) lands on a fresh line below the
// frozen progress line, instead of being painted over by it.
func progressPrinter(ctx context.Context, name string, contentCount func() int64, contentLength int64, interval time.Duration) {
	var rateValueFast, rateValueSlow tsrate.Value
	// tailscaled emits OutgoingFile.Sent updates at ~1 Hz, so most printer
	// ticks see no delta. With too short a half-life the displayed rate
	// roughly halves between updates and doubles back when one arrives,
	// looking jumpy. 5s keeps the swing under ~15% while still settling
	// within a few seconds of a real change.
	rateValueFast.HalfLife = 5 * time.Second  // smoothed rate for display
	rateValueSlow.HalfLife = 10 * time.Second // even slower, for ETA measurement
	var prevContentCount int64
	print := func() {
		currContentCount := contentCount()
		// Clamp so a regression (which shouldn't happen, but tsrate.Value.Add
		// panics on a negative count) can't take down the CLI.
		delta := max(currContentCount-prevContentCount, 0)
		rateValueFast.Add(float64(delta))
		rateValueSlow.Add(float64(delta))
		prevContentCount = currContentCount

		const vtRestartLine = "\r\x1b[K"
		fmt.Fprintf(os.Stderr, "%s%s    %s    %s",
			vtRestartLine,
			rightPad(name, 36),
			leftPad(formatIEC(float64(currContentCount), "B"), len("1023.00MiB")),
			leftPad(formatIEC(rateValueFast.Rate(), "B/s"), len("1023.00MiB/s")))
		if contentLength >= 0 {
			currContentCount = min(currContentCount, contentLength) // cap at 100%
			ratioRemain := float64(currContentCount) / float64(contentLength)
			etaStr := "ETA -"
			if rate := rateValueSlow.Rate(); rate > 0 {
				bytesRemain := float64(contentLength - currContentCount)
				secsRemain := bytesRemain / rate
				secs := int(min(max(0, secsRemain), 99*60*60+59+60+59))
				etaStr = fmt.Sprintf("ETA %02d:%02d:%02d", secs/60/60, (secs/60)%60, secs%60)
			}
			fmt.Fprintf(os.Stderr, "    %s    %s",
				leftPad(fmt.Sprintf("%0.2f%%", 100.0*ratioRemain), len("100.00%")),
				etaStr)
		}
	}

	const stuckAfter = 2 * time.Second
	var fullStartedAt time.Time // when we first observed currCount==contentLength with ~zero rate

	tc := time.NewTicker(interval)
	defer tc.Stop()
	print()
	for {
		select {
		case <-ctx.Done():
			print()
			fmt.Fprintln(os.Stderr)
			return
		case <-tc.C:
			print()
			if contentLength < 0 {
				continue
			}
			currCount := contentCount()
			rate := rateValueFast.Rate()
			if currCount >= contentLength && rate < 1 {
				if fullStartedAt.IsZero() {
					fullStartedAt = time.Now()
				} else if time.Since(fullStartedAt) >= stuckAfter {
					// Transfer is stuck at 100% with no movement. Stop
					// repainting so we don't keep clobbering anything the
					// rest of runCp prints (warnings, errors).
					fmt.Fprintln(os.Stderr)
					return
				}
			} else {
				fullStartedAt = time.Time{}
			}
		}
	}
}

func leftPad(s string, n int) string {
	s = truncateString(s, n)
	return strings.Repeat(" ", max(n-len(s), 0)) + s
}

func rightPad(s string, n int) string {
	s = truncateString(s, n)
	return s + strings.Repeat(" ", max(n-len(s), 0))
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return truncate.String(s, max(n-1, 0)) + "…"
}

func formatIEC(n float64, unit string) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%0.2f%s", n/(1<<0), unit)
	case n < 1<<20:
		return fmt.Sprintf("%0.2fKi%s", n/(1<<10), unit)
	case n < 1<<30:
		return fmt.Sprintf("%0.2fMi%s", n/(1<<20), unit)
	case n < 1<<40:
		return fmt.Sprintf("%0.2fGi%s", n/(1<<30), unit)
	default:
		return fmt.Sprintf("%0.2fTi%s", n/(1<<40), unit)
	}
}

func getTargetStableID(ctx context.Context, ipStr string) (id tailcfg.StableNodeID, isOffline bool, err error) {
	ip, err := netip.ParseAddr(ipStr)
	if err != nil {
		return "", false, err
	}

	st, err := localClient.Status(ctx)
	if err != nil {
		// This likely means tailscaled is unreachable or returned an error on /localapi/v0/status.
		return "", false, fmt.Errorf("获取本地状态失败：%w", err)
	}
	if st == nil {
		// Handle the case if the daemon returns nil with no error.
		return "", false, errors.New("没有可用的状态")
	}
	if st.Self == nil {
		// We have a status structure, but it doesn’t include Self info. Probably not connected.
		return "", false, errors.New("本地节点未配置或缺少自身信息")
	}

	// Find the PeerStatus that corresponds to ip.
	var foundPeer *ipnstate.PeerStatus
peerLoop:
	for _, ps := range st.Peer {
		for _, pip := range ps.TailscaleIPs {
			if pip == ip {
				foundPeer = ps
				break peerLoop
			}
		}
	}

	// If we didn’t find a matching peer at all:
	if foundPeer == nil {
		if !tsaddr.IsTailscaleIP(ip) {
			return "", false, fmt.Errorf("未知目标；%v 不是 Tailscale IP 地址", ip)
		}
		return "", false, errors.New("未知目标；不在你的 Tailnet 中")
	}

	// We found a peer. Decide whether we can send files to it:
	isOffline = !foundPeer.Online

	switch foundPeer.TaildropTarget {
	case ipnstate.TaildropTargetAvailable:
		return foundPeer.ID, isOffline, nil

	case ipnstate.TaildropTargetNoNetmapAvailable:
		return "", isOffline, errors.New("无法发送文件：此节点上没有可用的 netmap")

	case ipnstate.TaildropTargetIpnStateNotRunning:
		return "", isOffline, errors.New("无法发送文件：本地 Tailscale 未连接到 tailnet")

	case ipnstate.TaildropTargetMissingCap:
		return "", isOffline, errors.New("无法发送文件：缺少所需的 Taildrop 能力")

	case ipnstate.TaildropTargetOffline:
		// Don't gate on the server-reported Online bit (which lags reality
		// and isn't always accurate). runCp probes reachability itself with
		// TSMP pings.
		return foundPeer.ID, isOffline, nil

	case ipnstate.TaildropTargetNoPeerInfo:
		return "", isOffline, errors.New("无法发送文件：无效或无法识别的对等节点")

	case ipnstate.TaildropTargetUnsupportedOS:
		return "", isOffline, errors.New("无法发送文件：目标的操作系统不支持 Taildrop")

	case ipnstate.TaildropTargetNoPeerAPI:
		return "", isOffline, errors.New("无法发送文件：目标未通告文件共享 API")

	case ipnstate.TaildropTargetOwnedByOtherUser:
		return "", isOffline, errors.New("无法发送文件：该对等节点属于其他用户")

	case ipnstate.TaildropTargetUnknown:
		fallthrough
	default:
		return "", isOffline, fmt.Errorf("无法发送文件：原因未知或无法确定")
	}
}

const maxSniff = 4 << 20

func ext(b []byte) string {
	if len(b) < maxSniff && utf8.Valid(b) {
		return ".txt"
	}
	if exts, _ := mime.ExtensionsByType(http.DetectContentType(b)); len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// pickStdinFilename reads a bit of stdin to return a good filename
// for its contents. The returned Reader is the concatenation of the
// read and unread bits.
func pickStdinFilename() (name string, r *countingReader, err error) {
	sniff, err := io.ReadAll(io.LimitReader(os.Stdin, maxSniff))
	if err != nil {
		return "", nil, err
	}
	return "stdin" + ext(sniff), &countingReader{Reader: io.MultiReader(bytes.NewReader(sniff), os.Stdin)}, nil
}

type slowReader struct {
	r  io.Reader
	rl *rate.Limiter
}

func (r *slowReader) Read(p []byte) (n int, err error) {
	const burst = 4 << 10
	plen := min(len(p), burst)
	if r.rl == nil {
		r.rl = rate.NewLimiter(rate.Limit(1<<10), burst)
	}
	n, err = r.r.Read(p[:plen])
	r.rl.WaitN(context.Background(), n)
	return
}

func runCpTargets(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("使用 --targets 时参数无效")
	}
	fts, err := localClient.FileTargets(ctx)
	if err != nil {
		return err
	}
	for _, ft := range fts {
		n := ft.Node
		var detail string
		if n.Online != nil {
			if !*n.Online {
				detail = "离线"
			}
		} else {
			detail = "状态未知"
		}
		if detail != "" && n.LastSeen != nil {
			d := time.Since(*n.LastSeen)
			detail += fmt.Sprintf("；%v 前在线", d.Round(time.Minute))
		}
		if detail != "" {
			detail = "\t" + detail
		}
		printf("%s\t%s%s\n", n.Addresses[0].Addr(), n.ComputedName, detail)
	}
	return nil
}

// onConflict is a flag.Value for the --conflict flag's three string options.
type onConflict string

const (
	skipOnExist         onConflict = "skip"
	overwriteExisting   onConflict = "overwrite" //  Overwrite any existing file at the target location
	createNumberedFiles onConflict = "rename"    //  Create an alternately named file in the style of Chrome Downloads
)

func (v *onConflict) String() string { return string(*v) }

func (v *onConflict) Set(s string) error {
	if s == "" {
		*v = skipOnExist
		return nil
	}
	*v = onConflict(strings.ToLower(s))
	if *v != skipOnExist && *v != overwriteExisting && *v != createNumberedFiles {
		return fmt.Errorf("%q 不是 (skip|overwrite|rename) 之一", s)
	}
	return nil
}

var fileGetCmd = &ffcli.Command{
	Name:       "get",
	ShortUsage: "tailscale file get [--wait] [--verbose] [--conflict=(skip|overwrite|rename)] <target-directory>",
	ShortHelp:  "将文件移出 Tailscale 文件收件箱",
	Exec:       runFileGet,
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("get")
		fs.BoolVar(&fileGetArgs.wait, "wait", false, "若收件箱为空，则等待文件到达")
		fs.BoolVar(&fileGetArgs.loop, "loop", false, "以循环方式运行，文件到达时持续接收")
		fs.BoolVar(&fileGetArgs.verbose, "verbose", false, "详细输出")
		fs.Var(&fileGetArgs.conflict, "conflict", "`行为`"+` 当目标目录中已存在同名（冲突）文件时采取的动作。
	skip:       跳过冲突文件：将其留在 taildrop 收件箱并打印错误。接收所有不冲突的文件
	overwrite:  覆盖已存在的文件
	rename:     写入一个新的带编号后缀的文件名`)
		ffcomplete.Flag(fs, "conflict", ffcomplete.Fixed("skip", "overwrite", "rename"))
		return fs
	})(),
}

var fileGetArgs = struct {
	wait     bool
	loop     bool
	verbose  bool
	conflict onConflict
}{conflict: skipOnExist}

func numberedFileName(dir, name string, i int) string {
	ext := path.Ext(name)
	return filepath.Join(dir, fmt.Sprintf("%s (%d)%s",
		strings.TrimSuffix(name, ext),
		i, ext))
}

func openFileOrSubstitute(dir, base string, action onConflict) (*os.File, error) {
	targetFile := filepath.Join(dir, base)
	f, err := os.OpenFile(targetFile, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0644)
	if err == nil {
		return f, nil
	}
	// Something went wrong trying to open targetFile as a new file for writing.
	switch action {
	default:
		// This should not happen.
		return nil, fmt.Errorf("文件问题：如何解决此冲突？无人知晓。")
	case skipOnExist:
		if _, statErr := os.Stat(targetFile); statErr == nil {
			// we can stat a file at that path: so it already exists.
			return nil, fmt.Errorf("拒绝覆盖文件：%w", err)
		}
		return nil, fmt.Errorf("写入失败；%w", err)
	case overwriteExisting:
		// remove the target file and create it anew so we don't fall for an
		// attacker who symlinks a known target name to a file he wants changed.
		if err = os.Remove(targetFile); err != nil {
			return nil, fmt.Errorf("无法移除目标文件：%w", err)
		}
		if f, err = os.OpenFile(targetFile, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0644); err != nil {
			return nil, fmt.Errorf("无法覆盖：%w", err)
		}
		return f, nil
	case createNumberedFiles:
		// It's possible the target directory or filesystem isn't writable by us,
		// not just that the target file(s) already exists.  For now, give up after
		// a limited number of attempts.  In future, maybe distinguish this case
		// and follow in the style of https://tinyurl.com/chromium100
		maxAttempts := 100
		for i := 1; i < maxAttempts; i++ {
			if f, err = os.OpenFile(numberedFileName(dir, base, i), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0644); err == nil {
				return f, nil
			}
		}
		return nil, fmt.Errorf("找不到可用于写入 %v 的名称，最后一次尝试：%w", targetFile, err)
	}
}

func receiveFile(ctx context.Context, wf apitype.WaitingFile, dir string) (targetFile string, size int64, err error) {
	rc, size, err := localClient.GetWaitingFile(ctx, wf.Name)
	if err != nil {
		return "", 0, fmt.Errorf("打开收件箱文件 %q 失败：%w", wf.Name, err)
	}
	defer rc.Close()
	f, err := openFileOrSubstitute(dir, wf.Name, fileGetArgs.conflict)
	if err != nil {
		return "", 0, err
	}
	// Apply quarantine attribute before copying
	if err := quarantine.SetOnFile(f); err != nil {
		return "", 0, fmt.Errorf("对文件 %v 应用隔离属性失败：%v", f.Name(), err)
	}
	_, err = io.Copy(f, rc)
	if err != nil {
		f.Close()
		return "", 0, fmt.Errorf("写入 %v 失败：%v", f.Name(), err)
	}
	return f.Name(), size, f.Close()
}

func runFileGetOneBatch(ctx context.Context, dir string) []error {
	var wfs []apitype.WaitingFile
	var err error
	var errs []error
	for len(errs) == 0 {
		wfs, err = localClient.WaitingFiles(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("获取等待中的文件出错：%w", err))
			break
		}
		if len(wfs) != 0 || !(fileGetArgs.wait || fileGetArgs.loop) {
			break
		}
		if fileGetArgs.verbose {
			printf("正在等待文件...")
		}
		if err := waitForFile(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	deleted := 0
	for i, wf := range wfs {
		if len(errs) > 100 {
			// Likely, everything is broken.
			// Don't try to receive any more files in this batch.
			errs = append(errs, fmt.Errorf("runFileGetOneBatch() 中错误过多。有 %d 个文件未处理", len(wfs)-i))
			break
		}
		writtenFile, size, err := receiveFile(ctx, wf, dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if fileGetArgs.verbose {
			printf("已将 %v 写入为 %v（%d 字节）\n", wf.Name, writtenFile, size)
		}
		if err = localClient.DeleteWaitingFile(ctx, wf.Name); err != nil {
			errs = append(errs, fmt.Errorf("从收件箱删除 %q 出错：%v", wf.Name, err))
			continue
		}
		deleted++
	}
	if deleted == 0 && len(wfs) > 0 {
		// persistently stuck files are basically an error
		errs = append(errs, fmt.Errorf("已移动 %d/%d 个文件", deleted, len(wfs)))
	} else if fileGetArgs.verbose {
		printf("已移动 %d/%d 个文件\n", deleted, len(wfs))
	}
	return errs
}

func runFileGet(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("用法：tailscale file get <目标目录>")
	}
	log.SetFlags(0)

	dir := args[0]
	if dir == "/dev/null" {
		return wipeInbox(ctx)
	}

	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("%q 不是目录", dir)
	}
	if fileGetArgs.loop {
		for {
			errs := runFileGetOneBatch(ctx, dir)
			for _, err := range errs {
				outln(err)
			}
			if len(errs) > 0 {
				// It's possible whatever caused the error(s) (e.g. conflicting target file,
				// full disk, unwritable target directory) will re-occur if we try again so
				// let's back off and not busy loop on error.
				//
				// If we've been invoked as:
				//    tailscale file get --conflict=skip ~/Downloads
				// then any file coming in named the same as one in ~/Downloads will always
				// appear as an "error" until the user clears it, but other incoming files
				// should be receivable when they arrive, so let's not wait too long to
				// check again.
				time.Sleep(5 * time.Second)
			}
		}
	}
	errs := runFileGetOneBatch(ctx, dir)
	if len(errs) == 0 {
		return nil
	}
	for _, err := range errs[:len(errs)-1] {
		outln(err)
	}
	return errs[len(errs)-1]
}

func wipeInbox(ctx context.Context) error {
	if fileGetArgs.wait {
		return errors.New("不能对 /dev/null 目标使用 --wait")
	}
	wfs, err := localClient.WaitingFiles(ctx)
	if err != nil {
		return fmt.Errorf("获取等待中的文件出错：%w", err)
	}
	deleted := 0
	for _, wf := range wfs {
		if fileGetArgs.verbose {
			log.Printf("正在删除 %v ...", wf.Name)
		}
		if err := localClient.DeleteWaitingFile(ctx, wf.Name); err != nil {
			return fmt.Errorf("删除 %q 出错：%v", wf.Name, err)
		}
		deleted++
	}
	if fileGetArgs.verbose {
		log.Printf("已删除 %d 个文件", deleted)
	}
	return nil
}

func waitForFile(ctx context.Context) error {
	for {
		ff, err := localClient.AwaitWaitingFiles(ctx, time.Hour)
		if len(ff) > 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return err
		}
	}
}
