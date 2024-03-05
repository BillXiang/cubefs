package cmd

import (
	"fmt"
	"github.com/cubefs/cubefs/sdk/master"
	"github.com/spf13/cobra"
	"sort"
	"strconv"
)

const (
	cmdFlashNodeUse   = "flashNode [COMMAND]"
	cmdFlashNodeShort = "cluster flashNode info"
)

func newFlashNodeCommand(client *master.MasterClient) *cobra.Command {
	var cmd = &cobra.Command{
		Use:   cmdFlashNodeUse,
		Short: cmdFlashNodeShort,
	}
	cmd.AddCommand(
		newFlashNodeGetCmd(client),
		newFlashNodeDecommissionCmd(client),
		newFlashNodeListCmd(client),
		newFlashNodeSetStateCmd(client),
	)
	return cmd
}

func newFlashNodeGetCmd(client *master.MasterClient) *cobra.Command {
	var cmd = &cobra.Command{
		Use:   CliOpInfo + " [FlashNodeAddr] ",
		Short: "get flash node by addr",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			var err error
			defer func() {
				if err != nil {
					errout("Error: %v", err)
				}
			}()
			fn, err := client.NodeAPI().GetFlashNode(args[0])
			if err != nil {
				return
			}
			stdout("[Flash node]\n")
			stdout("%v\n", formatFlashNodeDetail(fn))
		},
	}
	return cmd
}

func newFlashNodeDecommissionCmd(client *master.MasterClient) *cobra.Command {
	var cmd = &cobra.Command{
		Use:   CliOpDecommission + " [FlashNodeAddr] ",
		Short: "decommission flash node by addr",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			var err error
			defer func() {
				if err != nil {
					errout("Error: %v", err)
				}
			}()
			result, err := client.NodeAPI().FlashNodeDecommission(args[0])
			if err != nil {
				return
			}
			stdout("%v\n", result)
		},
	}
	return cmd
}

func newFlashNodeListCmd(client *master.MasterClient) *cobra.Command {
	var detail, showAllFlashNodes bool
	var cmd = &cobra.Command{
		Use:   CliOpList,
		Short: "list all flash nodes",
		Args:  cobra.MinimumNArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			var err error
			defer func() {
				if err != nil {
					errout("Error: %v", err)
				}
			}()
			zoneFlashNodes, err := client.AdminAPI().GetAllFlashNodes(showAllFlashNodes)
			if err != nil {
				return
			}
			stdout("[Flash nodes]\n")
			stdout("%v\n", formatFlashNodeViewTableHeader())
			var row string
			for _, flashNodeViewInfos := range zoneFlashNodes {
				sort.Slice(flashNodeViewInfos, func(i, j int) bool {
					return flashNodeViewInfos[i].ID < flashNodeViewInfos[j].ID
				})
				for _, fn := range flashNodeViewInfos {
					var (
						hitRate = "N/A"
						evicts  = "N/A"
						limit   = "N/A"
					)
					if detail && fn.IsActive && fn.IsEnable {
						stat, err1 := getFlashNodeStat(fn.Addr, client.FlashNodeProfPort)
						if err1 == nil {
							hitRate = fmt.Sprintf("%.2f%%", stat.CacheStatus.HitRate*100)
							evicts = strconv.Itoa(stat.CacheStatus.Evicts)
							limit = strconv.FormatUint(stat.NodeLimit, 10)
						}
					}
					version := "N/A"
					commit := "N/A"
					versionInfo, e := getFlashNodeVersion(fn.Addr, client.FlashNodeProfPort)
					if e == nil {
						version = versionInfo.Version
						commit = versionInfo.CommitID
					}
					row = fmt.Sprintf(flashNodeViewTableRowPattern, fn.ZoneName, fn.ID, fn.Addr, version, commit,
						formatYesNo(fn.IsActive), fn.FlashGroupID, hitRate, evicts, limit, formatTime(fn.ReportTime.Unix()), fn.IsEnable)
					stdout("%v\n", row)
				}
			}
		},
	}
	cmd.Flags().BoolVar(&showAllFlashNodes, "showAllFlashNodes", true, fmt.Sprintf("show all flashNodes contain notActive or notEnable"))
	cmd.Flags().BoolVar(&detail, "detail", false, "show detail info")
	return cmd
}

func newFlashNodeSetStateCmd(client *master.MasterClient) *cobra.Command {
	var cmd = &cobra.Command{
		Use:   CliOpSet + " [FlashNodeAddr] " + " [state]",
		Short: "set flash node state",
		Args:  cobra.MinimumNArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			var err error
			defer func() {
				if err != nil {
					errout("Error: %v", err)
				}
			}()
			err = client.NodeAPI().SetFlashNodeState(args[0], args[1])
			if err != nil {
				stdout("%v", err)
				return
			}
			stdout("set flashNode state success\n")
		},
	}
	return cmd
}
