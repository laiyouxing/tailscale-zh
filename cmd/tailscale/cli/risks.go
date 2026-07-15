// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"errors"
	"flag"
	"strings"

	"tailscale.com/util/prompt"
	"tailscale.com/util/testenv"
)

var (
	riskTypes           []string
	riskLoseSSH         = registerRiskType("lose-ssh")
	riskMacAppConnector = registerRiskType("mac-app-connector")
	riskAll             = registerRiskType("all")
)

const riskMacAppConnectorMessage = `
你正在尝试在 macOS 上配置 app connector（应用连接器），由于系统限制，官方并不支持此操作。这可能会导致性能和可靠性问题。

请勿将 macOS 上的 app connector 用于任何关键业务用途。为了获得最佳体验，Linux 是唯一推荐用于 app connector 的平台。
`

func registerRiskType(riskType string) string {
	riskTypes = append(riskTypes, riskType)
	return riskType
}

// registerAcceptRiskFlag registers the --accept-risk flag. Accepted risks are accounted for
// in presentRiskToUser.
func registerAcceptRiskFlag(f *flag.FlagSet, acceptedRisks *string) {
	f.StringVar(acceptedRisks, "accept-risk", "", "接受风险并跳过以下风险类型的确认: "+strings.Join(riskTypes, ","))
}

// isRiskAccepted reports whether riskType is in the comma-separated list of
// risks in acceptedRisks.
func isRiskAccepted(riskType, acceptedRisks string) bool {
	for r := range strings.SplitSeq(acceptedRisks, ",") {
		if r == riskType || r == riskAll {
			return true
		}
	}
	return false
}

var errAborted = errors.New("已中止，未做任何更改")

// presentRiskToUser displays the risk message and waits for the user to cancel.
// It returns errorAborted if the user aborts. In tests it returns errAborted
// immediately unless the risk has been explicitly accepted.
func presentRiskToUser(riskType, riskMessage, acceptedRisks string) error {
	if isRiskAccepted(riskType, acceptedRisks) {
		return nil
	}
	if testenv.InTest() {
		return errAborted
	}
	outln(riskMessage)
	printf("要跳过此警告，请使用 --accept-risk=%s\n", riskType)

	if prompt.YesNo("继续？", false) {
		return nil
	}

	return errAborted
}
