// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_drive && !ts_mac_gui

package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/drive"
)

const (
	driveShareUsage   = "tailscale drive share <name> <path>"
	driveRenameUsage  = "tailscale drive rename <oldname> <newname>"
	driveUnshareUsage = "tailscale drive unshare <name>"
	driveListUsage    = "tailscale drive list"
)

func init() {
	maybeDriveCmd = driveCmd
}

func driveCmd() *ffcli.Command {
	return &ffcli.Command{
		Name:      "drive",
		ShortHelp: "与你的 tailnet 共享一个目录",
		ShortUsage: strings.Join([]string{
			driveShareUsage,
			driveRenameUsage,
			driveUnshareUsage,
			driveListUsage,
		}, "\n"),
		LongHelp:  buildShareLongHelp(),
		UsageFunc: usageFuncNoDefaultValues,
		Subcommands: []*ffcli.Command{
			{
				Name:       "share",
				ShortUsage: driveShareUsage,
				Exec:       runDriveShare,
				ShortHelp:  "[ALPHA] 创建或修改共享",
			},
			{
				Name:       "rename",
				ShortUsage: driveRenameUsage,
				ShortHelp:  "[ALPHA] 重命名共享",
				Exec:       runDriveRename,
			},
			{
				Name:       "unshare",
				ShortUsage: driveUnshareUsage,
				ShortHelp:  "[ALPHA] 移除共享",
				Exec:       runDriveUnshare,
			},
			{
				Name:       "list",
				ShortUsage: driveListUsage,
				ShortHelp:  "[ALPHA] 列出当前共享",
				Exec:       runDriveList,
			},
		},
	}
}

// runDriveShare is the entry point for the "tailscale drive share" command.
func runDriveShare(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: %s", driveShareUsage)
	}

	name, path := args[0], args[1]

	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	err = localClient.DriveShareSet(ctx, &drive.Share{
		Name: name,
		Path: absolutePath,
	})
	if err == nil {
		fmt.Printf("正在共享 %q，名为 %q\n", path, name)
	}
	return err
}

// runDriveUnshare is the entry point for the "tailscale drive unshare" command.
func runDriveUnshare(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: %s", driveUnshareUsage)
	}
	name := args[0]

	err := localClient.DriveShareRemove(ctx, name)
	if err == nil {
		fmt.Printf("已不再共享 %q\n", name)
	}
	return err
}

// runDriveRename is the entry point for the "tailscale drive rename" command.
func runDriveRename(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: %s", driveRenameUsage)
	}
	oldName := args[0]
	newName := args[1]

	err := localClient.DriveShareRename(ctx, oldName, newName)
	if err == nil {
		fmt.Printf("已将共享 %q 重命名为 %q\n", oldName, newName)
	}
	return err
}

// runDriveList is the entry point for the "tailscale drive list" command.
func runDriveList(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: %s", driveListUsage)
	}

	shares, err := localClient.DriveShareList(ctx)
	if err != nil {
		return err
	}

	longestName := 4 // "name"
	longestPath := 4 // "path"
	longestAs := 2   // "as" 即"作为"
	for _, share := range shares {
		if len(share.Name) > longestName {
			longestName = len(share.Name)
		}
		if len(share.Path) > longestPath {
			longestPath = len(share.Path)
		}
		if len(share.As) > longestAs {
			longestAs = len(share.As)
		}
	}
	formatString := fmt.Sprintf("%%-%ds    %%-%ds    %%s\n", longestName, longestPath)
	fmt.Printf(formatString, "名称", "路径", "作为")
	fmt.Printf(formatString, strings.Repeat("-", longestName), strings.Repeat("-", longestPath), strings.Repeat("-", longestAs))
	for _, share := range shares {
		fmt.Printf(formatString, share.Name, share.Path, share.As)
	}

	return nil
}

func buildShareLongHelp() string {
	longHelpAs := ""
	if drive.AllowShareAs() {
		longHelpAs = shareLongHelpAs
	}
	return fmt.Sprintf(shareLongHelpBase, longHelpAs)
}

var shareLongHelpBase = `Taildrive 允许你将目录与 tailnet 中的其他设备共享。

要共享文件夹，你的节点需要具有节点属性 "drive:share"。

要访问共享，你的节点需要具有节点属性 "drive:access"。

例如，要为所有成员节点启用共享与访问共享：

  "nodeAttrs": [
    {
      "target": ["autogroup:member"],
      "attr": [
        "drive:share",
        "drive:access",
      ],
    }]

每个共享由一个名称标识，并指向特定路径下的一个目录。例如，要以名称 "docs" 共享路径 /Users/me/Documents，你可以运行：

  $ tailscale drive share docs /Users/me/Documents

注意，系统会强制将共享名称转为小写，以避免不支持区分大小写文件名的客户端出现问题。

共享名称只能包含字母 a-z、下划线 _、括号 () 或空格。首尾的空格会被忽略。

所有 Tailscale 共享都有一个由 tailnet、机器名和共享名组成的全局唯一路径。例如，若上述共享创建于 tailnet "mydomain.com" 上的机器 "mylaptop"，则共享路径为：

  /mydomain.com/mylaptop/docs

要访问此共享，tailnet 中的其他设备可连接到运行于 100.100.100.100:8080 的 WebDAV 服务器上的上述路径，例如：

  http://100.100.100.100:8080/mydomain.com/mylaptop/docs

访问共享的权限通过 ACL 控制。例如，要授予 "home" 组对上述共享的只读访问权限，可使用以下 ACL 授权：

  "grants": [
    {
      "src": ["group:home"],
      "dst": ["mylaptop"],
      "app": {
        "tailscale.com/cap/drive": [{
          "shares": ["docs"],
          "access": "ro"
        }]
      }
    }]

每当 "home" 组中的任何人连接该共享时，他们就像在使用你的本地机器用户一样连接。他们将能够读取与你相同的文件，如果他们创建文件，这些文件将归你的用户所有。%s

在小型 tailnet 上，直接给所有用户完全访问自己共享的权限可能更方便。可通过以下授权实现：

  "grants": [
	{
	  "src": ["autogroup:member"],
	  "dst": ["autogroup:self"],
	  "app": {
	    "tailscale.com/cap/drive": [{
		  "shares": ["*"],
		  "access": "rw"
	    }]
	  }
	}]

你可以重命名共享，例如可通过以下命令重命名上述共享：

  $ tailscale drive rename docs newdocs

你可以按名称移除共享，例如可通过以下命令移除上述共享：

  $ tailscale drive unshare newdocs

你可通过以下命令获取当前已发布共享的列表：

  $ tailscale drive list`

const shareLongHelpAs = `

如果你想让某个共享以不同用户身份被访问，可以使用 sudo 来实现。例如，要以 "theuser" 身份创建上述共享，你可以运行：

  $ sudo -u theuser tailscale drive share docs /Users/theuser/Documents`
