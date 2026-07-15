// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_tailnetlock

package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	jsonv1 "encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/cmd/tailscale/cli/jsonoutput"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tka"
	"tailscale.com/tsconst"
	"tailscale.com/types/key"
	"tailscale.com/types/tkatype"
	"tailscale.com/util/prompt"
)

func init() {
	maybeTailnetLockCmd = func() *ffcli.Command { return tailnetLockCmd }
}

var tailnetLockCmd = &ffcli.Command{
	Name:       "lock",
	ShortUsage: "tailscale lock <subcommand> [arguments...]",
	ShortHelp:  "管理 tailnet lock 锁",
	LongHelp:   "管理 tailnet lock 锁",
	Subcommands: []*ffcli.Command{
		tlInitCmd,
		tlStatusCmd,
		tlAddCmd,
		tlRemoveCmd,
		tlSignCmd,
		tlDisableCmd,
		tlDisablementKDFCmd,
		tlLogCmd,
		tlLocalDisableCmd,
		tlRevokeKeysCmd,
	},
	Exec: runTailnetLockNoSubcommand,
}

func runTailnetLockNoSubcommand(ctx context.Context, args []string) error {
	// Detect & handle the deprecated command 'lock tskey-wrap'.
	if len(args) >= 2 && args[0] == "tskey-wrap" {
		return runTskeyWrapCmd(ctx, args[1:])
	}
	if len(args) > 0 {
		return fmt.Errorf("tailscale lock：未知子命令：%s", args[0])
	}

	return runTailnetLockStatus(ctx, args)
}

var nlInitArgs struct {
	numDisablements       int
	disablementForSupport bool
	confirm               bool
}

var tlInitCmd = &ffcli.Command{
	Name:       "init",
	ShortUsage: "tailscale lock init [--gen-disablement-for-support] --gen-disablements N <trusted-key>...",
	ShortHelp:  "初始化 tailnet lock 锁",
	LongHelp: strings.TrimSpace(`

'tailscale lock init' 命令为整个 tailnet 初始化 tailnet lock 锁。
所指定的 tailnet lock 密钥是初始被信任用于为节点签名或对
tailnet lock 进行进一步更改的密钥。

你可以通过在该节点上运行 'tailscale lock' 并复制该节点的
tailnet lock 密钥，来识别你希望信任的节点的 tailnet lock 密钥。

要禁用 tailnet lock 锁，请使用 'tailscale lock disable' 命令
并配合其中一个禁用密钥。
要生成的禁用密钥数量通过 --gen-disablements 标志指定。
初始化 tailnet lock 锁至少需要一个禁用密钥。

如果指定了 --gen-disablement-for-support，将额外生成一个禁用密钥
并传送给 Tailscale，支持团队可用其禁用 tailnet lock 锁。
我们建议设置此标志。

`),
	Exec: runTailnetLockInit,
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("lock init")
		fs.IntVar(&nlInitArgs.numDisablements, "gen-disablements", 1, "要生成的禁用密钥数量")
		fs.BoolVar(&nlInitArgs.disablementForSupport, "gen-disablement-for-support", false, "生成并传送一个供 Tailscale 支持使用的禁用密钥")
		fs.BoolVar(&nlInitArgs.confirm, "confirm", false, "不进行确认提示")
		return fs
	})(),
}

func runTailnetLockInit(ctx context.Context, args []string) error {
	st, err := localClient.TailnetLockStatus(ctx)
	if err != nil {
		return fixTailscaledConnectError(err)
	}
	if st.Enabled {
		return errors.New("tailnet lock 锁已启用")
	}

	// Parse initially-trusted keys & disablement values.
	keys, disablementValues, err := parseTLArgs(args, true, true)
	if err != nil {
		return err
	}

	// Common mistake: Not specifying the current node's key as one of the trusted keys.
	foundSelfKey := false
	for _, k := range keys {
		keyID, err := k.ID()
		if err != nil {
			return err
		}
		if bytes.Equal(keyID, st.PublicKey.KeyID()) {
			foundSelfKey = true
			break
		}
	}
	if !foundSelfKey {
		return errors.New("当前节点的 tailnet lock 密钥必须在初始化时被信任的密钥之一")
	}

	fmt.Println("你正在使用以下受信任的签名密钥初始化 tailnet lock 锁：")
	for _, k := range keys {
		fmt.Printf(" - tlpub:%x (%s key)\n", k.Public, k.Kind.String())
	}
	fmt.Println()

	if !nlInitArgs.confirm {
		fmt.Printf("将生成 %d 个禁用密钥。\n", nlInitArgs.numDisablements)
		if nlInitArgs.disablementForSupport {
			fmt.Println("将生成一个禁用密钥并传送给 Tailscale 支持。")
		}

		genSupportFlag := ""
		if nlInitArgs.disablementForSupport {
			genSupportFlag = "--gen-disablement-for-support "
		}
		fmt.Println("\n如果正确，请使用 --confirm 标志重新运行此命令：")
		fmt.Printf("\t%s lock init --confirm --gen-disablements %d %s%s", os.Args[0], nlInitArgs.numDisablements, genSupportFlag, strings.Join(args, " "))
		fmt.Println()
		return nil
	}

	var successMsg strings.Builder

	fmt.Fprintf(&successMsg, "已生成 %d 个禁用密钥，打印如下。请现在记下它们，它们将不会再次显示。\n", nlInitArgs.numDisablements)
	for range nlInitArgs.numDisablements {
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return err
		}
		fmt.Fprintf(&successMsg, "\tdisablement-secret:%X\n", secret[:])
		disablementValues = append(disablementValues, tka.DisablementKDF(secret[:]))
	}

	var supportDisablement []byte
	if nlInitArgs.disablementForSupport {
		supportDisablement = make([]byte, 32)
		if _, err := rand.Read(supportDisablement); err != nil {
			return err
		}
		disablementValues = append(disablementValues, tka.DisablementKDF(supportDisablement))
		fmt.Fprintln(&successMsg, "已生成供 Tailscale 支持使用的禁用密钥并传送给 Tailscale。")
	}

	// The state returned by TailnetLockInit likely doesn't contain the initialized state,
	// because that has to tick through from netmaps.
	if _, err := localClient.TailnetLockInit(ctx, keys, disablementValues, supportDisablement); err != nil {
		return err
	}

	fmt.Print(successMsg.String())
	fmt.Println("初始化完成。")
	return nil
}

var nlStatusArgs struct {
	json jsonoutput.SchemaVersion
}

var tlStatusCmd = &ffcli.Command{
	Name:       "status",
	ShortUsage: "tailscale lock status",
	ShortHelp:  "输出 tailnet lock 锁的状态",
	Exec:       runTailnetLockStatus,
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("lock status")
		fs.Var(&nlStatusArgs.json, "json", "以 JSON 格式输出")
		return fs
	})(),
}

func runTailnetLockStatus(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("tailscale lock status：意外参数")
	}

	st, err := localClient.TailnetLockStatus(ctx)
	if err != nil {
		return fixTailscaledConnectError(err)
	}

	if nlStatusArgs.json.IsSet {
		if nlStatusArgs.json.Version == 1 {
			return jsonoutput.PrintTailnetLockStatusJSONV1(os.Stdout, st)
		} else {
			return fmt.Errorf("无法识别的版本：%d", nlStatusArgs.json.Version)
		}
	}

	if st.Enabled {
		fmt.Println("Tailnet Lock 锁已启用。")
	} else {
		fmt.Println("Tailnet Lock 锁未启用。")
	}
	fmt.Println()

	if st.Enabled && st.NodeKey != nil && !st.PublicKey.IsZero() {
		if st.NodeKeySigned {
			fmt.Println("此节点在 Tailnet Lock 锁下可访问。节点签名：")
			fmt.Println(st.NodeKeySignature.String())
		} else {
			fmt.Println("此节点已被 Tailnet Lock 锁锁定，需要操作以建立连接。")
			fmt.Printf("在一个拥有受信任密钥的节点上运行以下命令：\n\ttailscale lock sign %v %s\n", st.NodeKey, st.PublicKey.CLIString())
		}
		fmt.Println()
	}

	if !st.PublicKey.IsZero() {
		fmt.Printf("此节点的 tailnet-lock 密钥：%s\n", st.PublicKey.CLIString())
		fmt.Println()
	}

	if st.Enabled && len(st.TrustedKeys) > 0 {
		fmt.Println("受信任的签名密钥：")
		for _, k := range st.TrustedKeys {
			var line strings.Builder
			line.WriteString("\t")
			line.WriteString(k.Key.CLIString())
			line.WriteString("\t")
			line.WriteString(fmt.Sprint(k.Votes))
			line.WriteString("\t")
			if k.Key == st.PublicKey {
				line.WriteString("（本机）")
			}
			if k.Metadata["purpose"] == "pre-auth key" {
				if preauthKeyID := k.Metadata["authkey_stableid"]; preauthKeyID != "" {
					line.WriteString("(pre-auth key ")
					line.WriteString(preauthKeyID)
					line.WriteString(")")
				} else {
					line.WriteString("(pre-auth key)")
				}
			}
			fmt.Println(line.String())
		}
	}

	if st.Enabled && len(st.FilteredPeers) > 0 {
		fmt.Println()
		fmt.Println("以下节点已被 tailnet lock 锁锁定，无法连接到其他节点：")
		for _, p := range st.FilteredPeers {
			var line strings.Builder
			line.WriteString("\t")
			line.WriteString(p.Name)
			line.WriteString("\t")
			for i, addr := range p.TailscaleIPs {
				line.WriteString(addr.String())
				if i < len(p.TailscaleIPs)-1 {
					line.WriteString(",")
				}
			}
			line.WriteString("\t")
			line.WriteString(string(p.StableID))
			line.WriteString("\t")
			line.WriteString(p.NodeKey.String())
			fmt.Println(line.String())
		}
	}

	return nil
}

var tlAddCmd = &ffcli.Command{
	Name:       "add",
	ShortUsage: "tailscale lock add <public-key>...",
	ShortHelp:  "向 tailnet lock 锁添加一个或多个受信任的签名密钥",
	Exec:       runTailnetLockAdd,
}

var nlRemoveArgs struct {
	resign bool
}

var tlRemoveCmd = &ffcli.Command{
	Name:       "remove",
	ShortUsage: "tailscale lock remove [--re-sign=false] <public-key>...",
	ShortHelp:  "从 tailnet lock 锁移除一个或多个受信任的签名密钥",
	Exec:       runTailnetLockRemove,
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("lock remove")
		fs.BoolVar(&nlRemoveArgs.resign, "re-sign", true, "为因移除受信任签名密钥而失效的签名重新签名")
		return fs
	})(),
}

func runTailnetLockRemove(ctx context.Context, args []string) error {
	removeKeys, _, err := parseTLArgs(args, true, false)
	if err != nil {
		return err
	}
	if len(removeKeys) == 0 {
		return fmt.Errorf("缺少参数，期望一个或多个 tailnet lock 密钥")
	}
	st, err := localClient.TailnetLockStatus(ctx)
	if err != nil {
		return fixTailscaledConnectError(err)
	}
	if !st.Enabled {
		return errors.New("tailnet lock 锁未启用")
	}
	if len(st.TrustedKeys) == 1 {
		return errors.New("无法移除最后一个受信任的签名密钥；请改用 'tailscale lock disable' 禁用 tailnet lock 锁，或在移除前先添加另一个签名密钥")
	}

	if nlRemoveArgs.resign {
		// Validate we are not removing trust in ourselves while resigning. This is because
		// we resign with our own key, so the signatures would be immediately invalid.
		for _, k := range removeKeys {
			kID, err := k.ID()
			if err != nil {
				return fmt.Errorf("computing KeyID for key %v: %w", k, err)
			}
			if bytes.Equal(st.PublicKey.KeyID(), kID) {
				return errors.New("重新签名时无法移除本地受信任的签名密钥；请在其他节点上运行命令，或使用 --re-sign=false")
			}
		}

		// Resign affected signatures for each of the keys we are removing.
		for _, k := range removeKeys {
			kID, _ := k.ID() // err already checked above
			sigs, err := localClient.TailnetLockAffectedSigs(ctx, kID)
			if err != nil {
				return fmt.Errorf("affected sigs for key %X: %w", kID, err)
			}

			for _, sigBytes := range sigs {
				var sig tka.NodeKeySignature
				if err := sig.Unserialize(sigBytes); err != nil {
					return fmt.Errorf("failed decoding signature: %w", err)
				}
				var nodeKey key.NodePublic
				if err := nodeKey.UnmarshalBinary(sig.Pubkey); err != nil {
					return fmt.Errorf("failed decoding pubkey for signature: %w", err)
				}

				// Safety: TailnetLockAffectedSigs() verifies all signatures before
				// successfully returning.
				rotationKey, _ := sig.UnverifiedWrappingPublic()
				if err := localClient.TailnetLockSign(ctx, nodeKey, []byte(rotationKey)); err != nil {
					return fmt.Errorf("failed to sign %v: %w", nodeKey, err)
				}
			}
		}
	} else {
		if isatty.IsTerminal(os.Stdout.Fd()) {
			fmt.Printf(`警告
在不重新签名节点的情况下移除签名密钥（--re-sign=false）
将导致由该密钥签名的任何节点被锁定于
Tailscale 网络之外。请谨慎操作。
`)
			if !prompt.YesNo("确定要移除签名密钥吗？", true) {
				fmt.Printf("中止移除签名密钥\n")
				os.Exit(0)
			}
		}
	}

	return localClient.TailnetLockModify(ctx, nil, removeKeys)
}

// parseTLArgs parses a slice of strings into slices of tka.Key & disablement
// values/secrets.
// The keys encoded in args should be specified using their key.NLPublic.MarshalText
// representation with an optional '?<votes>' suffix.
// Disablement values or secrets must be encoded in hex with a prefix of 'disablement:' or
// 'disablement-secret:'.
//
// If any element could not be parsed,
// a nil slice is returned along with an appropriate error.
func parseTLArgs(args []string, parseKeys, parseDisablements bool) (keys []tka.Key, disablements [][]byte, err error) {
	for i, a := range args {
		if parseDisablements && (strings.HasPrefix(a, "disablement:") || strings.HasPrefix(a, "disablement-secret:")) {
			b, err := hex.DecodeString(a[strings.Index(a, ":")+1:])
			if err != nil {
				return nil, nil, fmt.Errorf("解析禁用项 %d：%v", i+1, err)
			}
			disablements = append(disablements, b)
			continue
		}

		if !parseKeys {
			return nil, nil, fmt.Errorf("解析参数 %d：期望值以 \"disablement:\" 或 \"disablement-secret:\" 前缀开头，实际得到 %q", i+1, a)
		}

		var nlpk key.NLPublic
		spl := strings.SplitN(a, "?", 2)
		if err := nlpk.UnmarshalText([]byte(spl[0])); err != nil {
			return nil, nil, fmt.Errorf("解析密钥 %d：%v", i+1, err)
		}

		k := tka.Key{
			Kind:   tka.Key25519,
			Public: nlpk.Verifier(),
			Votes:  1,
		}
		if len(spl) > 1 {
			votes, err := strconv.Atoi(spl[1])
			if err != nil {
				return nil, nil, fmt.Errorf("解析密钥 %d 的票数：%v", i+1, err)
			}
			k.Votes = uint(votes)
		}
		keys = append(keys, k)
	}
	return keys, disablements, nil
}

func runTailnetLockAdd(ctx context.Context, addArgs []string) error {
	addKeys, _, err := parseTLArgs(addArgs, true, false)
	if err != nil {
		return err
	}
	if len(addKeys) == 0 {
		return fmt.Errorf("缺少参数，期望一个或多个 tailnet lock 密钥")
	}

	st, err := localClient.TailnetLockStatus(ctx)
	if err != nil {
		return fixTailscaledConnectError(err)
	}
	if !st.Enabled {
		return errors.New("tailnet lock 锁未启用")
	}

	if err := localClient.TailnetLockModify(ctx, addKeys, nil); err != nil {
		return err
	}
	return nil
}

var tlSignCmd = &ffcli.Command{
	Name:       "sign",
	ShortUsage: "tailscale lock sign <node-key> [<rotation-key>]\ntailscale lock sign <auth-key>",
	ShortHelp:  "为一个节点或预批准的认证密钥签名",
	LongHelp: `或者：
  - 为一个节点密钥签名，并将签名传送到协调
    服务器，或者
  - 为一个预批准的认证密钥签名，以可用于
    在 tailnet lock 锁下启动节点的形式打印出来

如果任一密钥参数以 "file:" 开头，则该密钥从
参数后缀指定路径的文件中读取。`,
	Exec: runTailnetLockSign,
}

func runTailnetLockSign(ctx context.Context, args []string) error {
	// If any of the arguments start with "file:", replace that argument
	// with the contents of the file. We do this early, before the check
	// to see if the first argument is an auth key.
	for i, arg := range args {
		if filename, ok := strings.CutPrefix(arg, "file:"); ok {
			b, err := os.ReadFile(filename)
			if err != nil {
				return err
			}
			args[i] = strings.TrimSpace(string(b))
		}
	}

	if len(args) > 0 && strings.HasPrefix(args[0], "tskey-auth-") {
		return runTskeyWrapCmd(ctx, args)
	}

	var (
		nodeKey     key.NodePublic
		rotationKey key.NLPublic
	)

	if len(args) == 0 || len(args) > 2 {
		return errors.New("用法：tailscale lock sign <node-key> [<rotation-key>]")
	}
	if err := nodeKey.UnmarshalText([]byte(args[0])); err != nil {
		return fmt.Errorf("解码 node-key：%w", err)
	}
	if len(args) > 1 {
		if err := rotationKey.UnmarshalText([]byte(args[1])); err != nil {
			return fmt.Errorf("解码 rotation-key：%w", err)
		}
	}

	err := localClient.TailnetLockSign(ctx, nodeKey, []byte(rotationKey.Verifier()))
	// Provide a better help message for when someone clicks through the signing flow
	// on the wrong device.
	if err != nil && strings.Contains(err.Error(), tsconst.TailnetLockNotTrustedMsg) {
		fmt.Fprintln(Stderr, "错误：此设备无法进行签名，因为它没有受信任的 tailnet lock 密钥。")
		fmt.Fprintln(Stderr)
		fmt.Fprintln(Stderr, "请改在签名设备上重试。Tailnet 管理员可在管理后台查看签名设备。")
		fmt.Fprintln(Stderr)
	}
	return err
}

var tlDisableCmd = &ffcli.Command{
	Name:       "disable",
	ShortUsage: "tailscale lock disable <disablement-secret>",
	ShortHelp:  "使用一个禁用密钥来关闭整个 tailnet 的 tailnet lock 锁",
	LongHelp: strings.TrimSpace(`

'tailscale lock disable' 命令使用指定的禁用
密钥来禁用 tailnet lock 锁。

如果重新启用 tailnet lock 锁，可生成新的禁用密钥。

一旦此密钥被使用，它已被分发到
tailnet 中的所有节点，应被视为公开。

`),
	Exec: runTailnetLockDisable,
}

func runTailnetLockDisable(ctx context.Context, args []string) error {
	_, secrets, err := parseTLArgs(args, false, true)
	if err != nil {
		return err
	}
	if len(secrets) != 1 {
		return errors.New("用法：tailscale lock disable <disablement-secret>")
	}
	return localClient.TailnetLockDisable(ctx, secrets[0])
}

var tlLocalDisableCmd = &ffcli.Command{
	Name:       "local-disable",
	ShortUsage: "tailscale lock local-disable",
	ShortHelp:  "仅为此节点禁用 tailnet lock 锁",
	LongHelp: strings.TrimSpace(`

'tailscale lock local-disable' 命令仅为
当前节点禁用 tailnet lock 锁。

如果当前节点被锁定，这并不意味着它能在启用了
tailnet lock 锁的 tailnet 中发起连接。而是意味着
当前节点将接受来自 tailnet 中
被锁定的其他节点的流量。

`),
	Exec: runTailnetLockLocalDisable,
}

func runTailnetLockLocalDisable(ctx context.Context, args []string) error {
	return localClient.TailnetLockForceLocalDisable(ctx)
}

var tlDisablementKDFCmd = &ffcli.Command{
	Name:       "disablement-kdf",
	ShortUsage: "tailscale lock disablement-kdf <hex-encoded-disablement-secret>",
	ShortHelp:  "根据禁用密钥计算禁用值（仅限高级用户）",
	LongHelp:   "根据禁用密钥计算禁用值（仅限高级用户）",
	Exec:       runTailnetLockDisablementKDF,
}

func runTailnetLockDisablementKDF(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("用法：tailscale lock disablement-kdf <hex-encoded-disablement-secret>")
	}
	secret, err := hex.DecodeString(args[0])
	if err != nil {
		return err
	}
	fmt.Printf("disablement:%x\n", tka.DisablementKDF(secret))
	return nil
}

var nlLogArgs struct {
	limit int
	json  jsonoutput.SchemaVersion
}

var tlLogCmd = &ffcli.Command{
	Name:       "log",
	ShortUsage: "tailscale lock log [--limit N]",
	ShortHelp:  "列出应用于 tailnet lock 锁的变更",
	LongHelp:   "列出应用于 tailnet lock 锁的变更",
	Exec:       runTailnetLockLog,
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("lock log")
		fs.IntVar(&nlLogArgs.limit, "limit", 50, "要列出的最大更新数量")
		fs.Var(&nlLogArgs.json, "json", "以 JSON 格式输出")
		return fs
	})(),
}

func nlDescribeUpdate(update ipnstate.TailnetLockUpdate, color bool) (string, error) {
	terminalYellow := ""
	terminalClear := ""
	if color {
		terminalYellow = "\x1b[33m"
		terminalClear = "\x1b[0m"
	}

	var stanza strings.Builder
	printKey := func(key *tka.Key, prefix string) {
		fmt.Fprintf(&stanza, "%sType: %s\n", prefix, key.Kind.String())
		if keyID, err := key.ID(); err == nil {
			fmt.Fprintf(&stanza, "%sKeyID: tlpub:%x\n", prefix, keyID)
		} else {
			// Older versions of the client shouldn't explode when they encounter an
			// unknown key type.
			fmt.Fprintf(&stanza, "%sKeyID：<错误：%v>\n", prefix, err)
		}
		if key.Meta != nil {
			fmt.Fprintf(&stanza, "%sMetadata: %+v\n", prefix, key.Meta)
		}
	}

	var aum tka.AUM
	if err := aum.Unserialize(update.Raw); err != nil {
		return "", fmt.Errorf("解码：%w", err)
	}

	tkaHead, err := aum.Hash().MarshalText()
	if err != nil {
		return "", fmt.Errorf("解码 AUM 哈希：%w", err)
	}
	fmt.Fprintf(&stanza, "%supdate %s (%s)%s\n", terminalYellow, string(tkaHead), update.Change, terminalClear)

	switch update.Change {
	case tka.AUMAddKey.String():
		printKey(aum.Key, "")
	case tka.AUMRemoveKey.String():
		fmt.Fprintf(&stanza, "KeyID: tlpub:%x\n", aum.KeyID)

	case tka.AUMUpdateKey.String():
		fmt.Fprintf(&stanza, "KeyID: tlpub:%x\n", aum.KeyID)
		if aum.Votes != nil {
			fmt.Fprintf(&stanza, "Votes: %d\n", aum.Votes)
		}
		if aum.Meta != nil {
			fmt.Fprintf(&stanza, "Metadata: %+v\n", aum.Meta)
		}

	case tka.AUMCheckpoint.String():
		fmt.Fprintln(&stanza, "禁用值：")
		for _, v := range aum.State.DisablementValues {
			fmt.Fprintf(&stanza, " - %x\n", v)
		}
		fmt.Fprintln(&stanza, "密钥：")
		for _, k := range aum.State.Keys {
			printKey(&k, "  ")
		}

	default:
		// Print a JSON encoding of the AUM as a fallback.
		e := jsonv1.NewEncoder(&stanza)
		e.SetIndent("", "\t")
		if err := e.Encode(aum); err != nil {
			return "", err
		}
		stanza.WriteRune('\n')
	}

	return stanza.String(), nil
}

func runTailnetLockLog(ctx context.Context, args []string) error {
	st, err := localClient.TailnetLockStatus(ctx)
	if err != nil {
		return fixTailscaledConnectError(err)
	}
	if !st.Enabled {
		return errors.New("Tailnet Lock 锁未启用")
	}

	updates, err := localClient.TailnetLockLog(ctx, nlLogArgs.limit)
	if err != nil {
		return fixTailscaledConnectError(err)
	}

	out, useColor := colorableOutput()

	return printTailnetLockLog(updates, out, nlLogArgs.json, useColor)
}

func printTailnetLockLog(updates []ipnstate.NetworkLockUpdate, out io.Writer, jsonSchema jsonoutput.SchemaVersion, useColor bool) error {
	if jsonSchema.IsSet {
		if jsonSchema.Version == 1 {
			return jsonoutput.PrintTailnetLockLogJSONV1(out, updates)
		} else {
			return fmt.Errorf("无法识别的版本：%d", jsonSchema.Version)
		}
	}

	for _, update := range updates {
		stanza, err := nlDescribeUpdate(update, useColor)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, stanza)
	}
	return nil
}

func runTskeyWrapCmd(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("用法：lock tskey-wrap <tailscale pre-auth key>")
	}
	if strings.Contains(args[0], "--TL") {
		return errors.New("错误：提供的密钥已被包装")
	}

	st, err := localClient.StatusWithoutPeers(ctx)
	if err != nil {
		return fixTailscaledConnectError(err)
	}

	return wrapAuthKey(ctx, args[0], st)
}

func wrapAuthKey(ctx context.Context, keyStr string, status *ipnstate.Status) error {
	// Generate a separate tailnet-lock key just for the credential signature.
	// We use the free-form meta strings to mark a little bit of metadata about this
	// key.
	priv := key.NewNLPrivate()
	m := map[string]string{
		"purpose":            "pre-auth key",
		"wrapper_stableid":   string(status.Self.ID),
		"wrapper_createtime": fmt.Sprint(time.Now().Unix()),
	}
	if strings.HasPrefix(keyStr, "tskey-auth-") && strings.Index(keyStr[len("tskey-auth-"):], "-") > 0 {
		// We don't want to accidentally embed the nonce part of the authkey in
		// the event the format changes. As such, we make sure its in the format we
		// expect (tskey-auth-<stableID, inc CNTRL suffix>-nonce) before we parse
		// out and embed the stableID.
		s := strings.TrimPrefix(keyStr, "tskey-auth-")
		m["authkey_stableid"] = s[:strings.Index(s, "-")]
	}
	k := tka.Key{
		Kind:   tka.Key25519,
		Public: priv.Public().Verifier(),
		Votes:  1,
		Meta:   m,
	}

	wrapped, err := localClient.TailnetLockWrapPreauthKey(ctx, keyStr, priv)
	if err != nil {
		return fmt.Errorf("包装失败：%w", err)
	}
	if err := localClient.TailnetLockModify(ctx, []tka.Key{k}, nil); err != nil {
		return fmt.Errorf("添加密钥失败：%w", err)
	}

	fmt.Println(wrapped)
	return nil
}

var tlRevokeKeysArgs struct {
	cosign   bool
	finish   bool
	forkFrom string
}

var tlRevokeKeysCmd = &ffcli.Command{
	Name:       "revoke-keys",
	ShortUsage: "tailscale lock revoke-keys <tailnet-lock-key>...\n  revoke-keys [--cosign] [--finish] <recovery-blob>",
	ShortHelp:  "吊销已被泄露的 tailnet-lock 密钥",
	LongHelp: `追溯吊销指定的 tailnet lock 密钥（tlpub:abc）。

被吊销的密钥将被禁止在未来使用。任何此前由被吊销
密钥签名的节点将失去授权，必须重新签名。

吊销是一个多步骤过程，需要多个签名节点对吊销进行 ` + "`--cosign`" + `。
如果密钥未被泄露，请改用 ` + "`tailscale lock remove`" + `。

1. 首先，运行 ` + "`tailscale revoke-keys <tlpub-keys>`" + ` 并指定要吊销的 tailnet lock 密钥。
2. 在其他签名节点上重新运行 ` + "`revoke-keys`" + ` 输出的 ` + "`--cosign`" + ` 命令。在下一个
   签名节点上依次使用最近的命令输出。
3. 一旦 ` + "`--cosign`" + ` 的数量大于被吊销密钥的数量，
   最后再运行一次该命令，使用 ` + "`--finish`" + ` 替代 ` + "`--cosign`" + `。`,
	Exec: runTailnetLockRevokeKeys,
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("lock revoke-keys")
		fs.BoolVar(&tlRevokeKeysArgs.cosign, "cosign", false, "使用本设备的 tailnet lock 密钥和提供的恢复 blob 继续生成恢复")
		fs.BoolVar(&tlRevokeKeysArgs.finish, "finish", false, "通过传送吊销来完成恢复过程")
		fs.StringVar(&tlRevokeKeysArgs.forkFrom, "fork-from", "", "要重写自的父 AUM 哈希（仅限高级用户）")
		return fs
	})(),
}

func runTailnetLockRevokeKeys(ctx context.Context, args []string) error {
	// First step in the process
	if !tlRevokeKeysArgs.cosign && !tlRevokeKeysArgs.finish {
		revokeKeys, _, err := parseTLArgs(args, true, false)
		if err != nil {
			return err
		}

		if len(revokeKeys) == 0 {
			return fmt.Errorf("缺少参数，期望一个或多个 tailnet lock 密钥")
		}

		keyIDs := make([]tkatype.KeyID, len(revokeKeys))
		for i, k := range revokeKeys {
			keyIDs[i], err = k.ID()
			if err != nil {
				return fmt.Errorf("生成 keyID：%v", err)
			}
		}

		var forkFrom tka.AUMHash
		if tlRevokeKeysArgs.forkFrom != "" {
			if len(tlRevokeKeysArgs.forkFrom) == (len(forkFrom) * 2) {
				// Hex-encoded: like the output of the lock log command.
				b, err := hex.DecodeString(tlRevokeKeysArgs.forkFrom)
				if err != nil {
					return fmt.Errorf("无效的 fork-from 哈希：%v", err)
				}
				copy(forkFrom[:], b)
			} else {
				if err := forkFrom.UnmarshalText([]byte(tlRevokeKeysArgs.forkFrom)); err != nil {
					return fmt.Errorf("无效的 fork-from 哈希：%v", err)
				}
			}
		}

		aumBytes, err := localClient.TailnetLockGenRecoveryAUM(ctx, keyIDs, forkFrom)
		if err != nil {
			return fmt.Errorf("生成恢复 AUM 失败：%w", err)
		}

		fmt.Printf(`在另一台拥有受信任 tailnet lock 密钥的机器上运行以下命令：
	%s lock revoke-keys --cosign %X
`, os.Args[0], aumBytes)
		return nil
	}

	// If we got this far, we need to co-sign the AUM and/or transmit it for distribution.
	b, err := hex.DecodeString(args[0])
	if err != nil {
		return fmt.Errorf("解析十六进制：%v", err)
	}
	var recoveryAUM tka.AUM
	if err := recoveryAUM.Unserialize(b); err != nil {
		return fmt.Errorf("解码恢复 AUM：%v", err)
	}

	if tlRevokeKeysArgs.cosign {
		aumBytes, err := localClient.TailnetLockCosignRecoveryAUM(ctx, recoveryAUM)
		if err != nil {
			return fmt.Errorf("共同签名恢复 AUM 失败：%w", err)
		}

		fmt.Printf(`共同签名已成功完成。

要累积额外的签名，请在另一台拥有受信任 tailnet lock 密钥的机器上运行以下命令：
	%s lock revoke-keys --cosign %X

或者，如果你已完成共同签名，通过运行以下命令完成恢复：
	%s lock revoke-keys --finish %X
`, os.Args[0], aumBytes, os.Args[0], aumBytes)
	}

	if tlRevokeKeysArgs.finish {
		if err := localClient.TailnetLockSubmitRecoveryAUM(ctx, recoveryAUM); err != nil {
			return fmt.Errorf("提交恢复 AUM 失败：%w", err)
		}
		fmt.Println("恢复已完成。")
	}

	return nil
}
