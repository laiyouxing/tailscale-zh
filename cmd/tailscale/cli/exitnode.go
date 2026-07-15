// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/kballard/go-shellquote"
	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/envknob"
	"tailscale.com/feature/buildfeatures"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/util/slicesx"
)

func exitNodeCmd() *ffcli.Command {
	return &ffcli.Command{
		Name:       "exit-node",
		ShortUsage: "tailscale exit-node [flags]",
		ShortHelp:  "显示你的 tailnet 上配置为出口节点的设备",
		Subcommands: append([]*ffcli.Command{
			{
				Name:       "list",
				ShortUsage: "tailscale exit-node list [flags]",
				ShortHelp:  "显示出口节点",
				Exec:       runExitNodeList,
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("list")
					fs.StringVar(&exitNodeArgs.filter, "filter", "", "按国家筛选出口节点")
					return fs
				})(),
			},
			{
				Name:       "suggest",
				ShortUsage: "tailscale exit-node suggest",
				ShortHelp:  "推荐当前最佳的可用出口节点",
				Exec:       runExitNodeSuggest,
				FlagSet: (func() *flag.FlagSet {
					fs := newFlagSet("suggest")
					if buildfeatures.HasRouteCheck {
						fs.BoolVar(&exitNodeArgs.probe, "force-probe", false, hidden+"在推荐前执行一次 routecheck 探测")
					}
					return fs
				})(),
			}},
			(func() []*ffcli.Command {
				if !envknob.UseWIPCode() {
					return nil
				}
				return []*ffcli.Command{
					{
						Name:       "connect",
						ShortUsage: "tailscale exit-node connect",
						ShortHelp:  "连接至最近使用的出口节点",
						Exec:       exitNodeSetUse(true),
					},
					{
						Name:       "disconnect",
						ShortUsage: "tailscale exit-node disconnect",
						ShortHelp:  "与当前出口节点断开连接（如果有）",
						Exec:       exitNodeSetUse(false),
					},
				}
			})()...),
	}
}

var exitNodeArgs struct {
	filter string
	probe  bool
}

func exitNodeSetUse(wantOn bool) func(ctx context.Context, args []string) error {
	return func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return errors.New("出现意料之外的非标志参数")
		}
		err := localClient.SetUseExitNode(ctx, wantOn)
		if err != nil {
			if !wantOn {
				pref, err := localClient.GetPrefs(ctx)
				if err == nil && pref.ExitNodeID == "" {
					// Two processes concurrently turned it off.
					return nil
				}
			}
		}
		return err
	}
}

// runExitNodeList returns a formatted list of exit nodes for a tailnet.
// If the exit node has location and priority data, only the highest
// priority node for each city location is shown to the user.
// If the country location has more than one city, an 'Any' city
// is returned for the country, which lists the highest priority
// node in that country.
// For countries without location data, each exit node is displayed.
func runExitNodeList(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("'tailscale exit-node list' 出现了意料之外的非标志参数")
	}
	getStatus := localClient.Status
	st, err := getStatus(ctx)
	if err != nil {
		return fixTailscaledConnectError(err)
	}

	var peers []*ipnstate.PeerStatus
	for _, ps := range st.Peer {
		if !ps.ExitNodeOption {
			// We only show exit nodes under the exit-node subcommand.
			continue
		}
		peers = append(peers, ps)
	}

	if len(peers) == 0 {
		return errors.New("未找到任何出口节点")
	}

	filteredPeers := filterFormatAndSortExitNodes(peers, exitNodeArgs.filter)

	if len(filteredPeers.Countries) == 0 && exitNodeArgs.filter != "" {
		return fmt.Errorf("未找到属于 %q 的出口节点", exitNodeArgs.filter)
	}

	w := tabwriter.NewWriter(Stdout, 10, 5, 5, ' ', 0)
	defer w.Flush()
	fmt.Fprintf(w, "\n %s\t%s\t%s\t%s\t%s\t", "IP", "主机名", "国家", "城市", "状态")
	for _, country := range filteredPeers.Countries {
		for _, city := range country.Cities {
			for _, peer := range city.Peers {
				fmt.Fprintf(w, "\n %s\t%s\t%s\t%s\t%s\t", peer.TailscaleIPs[0], strings.Trim(peer.DNSName, "."), cmp.Or(country.Name, "-"), cmp.Or(city.Name, "-"), peerStatus(peer))
			}
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "# 要查看某个国家的完整出口节点列表，请使用 `tailscale exit-node list --filter=` 后跟国家名称。")
	fmt.Fprintln(w, "# 要使用某个出口节点，请使用 `tailscale set --exit-node=` 后跟 IP 或主机名。")
	if hasAnyExitNodeSuggestions(peers) {
		fmt.Fprintln(w, "# 要让 Tailscale 推荐一个出口节点，请使用 `tailscale exit-node suggest`。")
	}
	return nil
}

// runExitNodeSuggest returns a suggested exit node ID to connect to and shows the chosen exit node tailcfg.StableNodeID.
// If there are no derp based exit nodes to choose from or there is a failure in finding a suggestion, the command will return an error indicating so.
func runExitNodeSuggest(ctx context.Context, args []string) error {
	suggestExitNode := localClient.SuggestExitNode
	if exitNodeArgs.probe {
		suggestExitNode = localClient.SuggestExitNodeWithProbe
	}
	res, err := suggestExitNode(ctx)
	if err != nil {
		return fmt.Errorf("推荐出口节点: %w", err)
	}
	if res.ID == "" {
		fmt.Println("当前没有可用的出口节点推荐。")
		return nil
	}
	fmt.Printf("推荐的出口节点: %v\n要接受此推荐，请使用 `tailscale set --exit-node=%v`。\n", res.Name, shellquote.Join(res.Name))
	return nil
}

func hasAnyExitNodeSuggestions(peers []*ipnstate.PeerStatus) bool {
	for _, peer := range peers {
		if peer.HasCap(tailcfg.NodeAttrSuggestExitNode) {
			return true
		}
	}
	return false
}

// peerStatus returns a string representing the current state of
// a peer. If there is no notable state, a - is returned.
func peerStatus(peer *ipnstate.PeerStatus) string {
	if !peer.Active {
		lastseen := lastSeenFmt(peer.LastSeen)

		if peer.ExitNode {
			return "已选中但离线" + lastseen
		}
		if !peer.Online {
			return "离线" + lastseen
		}
	}

	if peer.ExitNode {
		return "已选中"
	}

	return "-"
}

type filteredExitNodes struct {
	Countries []*filteredCountry
}

type filteredCountry struct {
	Name   string
	Cities []*filteredCity
}

type filteredCity struct {
	Name  string
	Peers []*ipnstate.PeerStatus
}

// filterFormatAndSortExitNodes filters and sorts exit nodes into
// alphabetical order, by country, city and then by priority if
// present.
//
// If an exit node has location data, and the country has more than
// one city, an `Any` city is added to the country that contains the
// highest priority exit node within that country.
//
// For exit nodes without location data, their country fields are
// defined as the empty string to indicate that the data is not available.
func filterFormatAndSortExitNodes(peers []*ipnstate.PeerStatus, filterBy string) filteredExitNodes {
	// first get peers into some fixed order, as code below doesn't break ties
	// and our input comes from a random range-over-map.
	slices.SortFunc(peers, func(a, b *ipnstate.PeerStatus) int {
		return strings.Compare(a.DNSName, b.DNSName)
	})

	countries := make(map[string]*filteredCountry)
	cities := make(map[string]*filteredCity)
	for _, ps := range peers {
		loc := ps.Location
		if loc == nil {
			loc = &tailcfg.Location{}
		}

		if filterBy != "" && !strings.EqualFold(loc.Country, filterBy) {
			continue
		}

		co, ok := countries[loc.CountryCode]
		if !ok {
			co = &filteredCountry{
				Name: loc.Country,
			}
			countries[loc.CountryCode] = co
		}

		ci, ok := cities[loc.CityCode]
		if !ok {
			ci = &filteredCity{
				Name: loc.City,
			}
			cities[loc.CityCode] = ci
			co.Cities = append(co.Cities, ci)
		}
		ci.Peers = append(ci.Peers, ps)
	}

	filteredExitNodes := filteredExitNodes{
		Countries: slicesx.MapValues(countries),
	}

	for _, country := range filteredExitNodes.Countries {
		if country.Name == "" {
			// Countries without location data should not
			// be filtered further.
			continue
		}

		var countryAnyPeer []*ipnstate.PeerStatus
		for _, city := range country.Cities {
			sortPeersByPriority(city.Peers)
			countryAnyPeer = append(countryAnyPeer, city.Peers...)
			var reducedCityPeers []*ipnstate.PeerStatus
			for i, peer := range city.Peers {
				if filterBy != "" {
					// If the peers are being filtered, we return all peers to the user.
					reducedCityPeers = append(reducedCityPeers, city.Peers...)
					break
				}
				// If the peers are not being filtered, we only return the highest priority peer and any peer that
				// is currently the active exit node.
				if i == 0 || peer.ExitNode {
					reducedCityPeers = append(reducedCityPeers, peer)
				}
			}
			city.Peers = reducedCityPeers
		}
		sortByCityName(country.Cities)
		sortPeersByPriority(countryAnyPeer)

		if len(country.Cities) > 1 {
			// For countries with more than one city, we want to return the
			// option of the best peer for that country.
			country.Cities = append([]*filteredCity{
				{
					Name:  "Any",
					Peers: []*ipnstate.PeerStatus{countryAnyPeer[0]},
				},
			}, country.Cities...)
		}
	}
	sortByCountryName(filteredExitNodes.Countries)

	return filteredExitNodes
}

// sortPeersByPriority sorts a slice of PeerStatus
// by location.Priority, in order of highest priority.
func sortPeersByPriority(peers []*ipnstate.PeerStatus) {
	slices.SortStableFunc(peers, func(a, b *ipnstate.PeerStatus) int {
		return cmp.Compare(b.Location.Priority, a.Location.Priority)
	})
}

// sortByCityName sorts a slice of filteredCity alphabetically
// by name. The '-' used to indicate no location data will always
// be sorted to the front of the slice.
func sortByCityName(cities []*filteredCity) {
	slices.SortStableFunc(cities, func(a, b *filteredCity) int { return strings.Compare(a.Name, b.Name) })
}

// sortByCountryName sorts a slice of filteredCountry alphabetically
// by name. The '-' used to indicate no location data will always
// be sorted to the front of the slice.
func sortByCountryName(countries []*filteredCountry) {
	slices.SortStableFunc(countries, func(a, b *filteredCountry) int { return strings.Compare(a.Name, b.Name) })
}
