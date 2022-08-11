// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package talos

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/talos-systems/talos/pkg/machinery/client"
)

var rebootCmdFlags struct {
	mode   string
	noWait bool
	debug  bool
}

// rebootCmd represents the reboot command.
var rebootCmd = &cobra.Command{
	Use:   "reboot",
	Short: "Reboot a node",
	Long:  ``,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if rebootCmdFlags.debug {
			rebootCmdFlags.noWait = false
		}

		var opts []client.RebootMode

		switch rebootCmdFlags.mode {
		// skips kexec and reboots with power cycle
		case "powercycle":
			opts = append(opts, client.WithPowerCycle)
		case "default":
		default:
			return fmt.Errorf("invalid reboot mode: %q", rebootCmdFlags.mode)
		}

		if rebootCmdFlags.noWait {
			return WithClient(func(ctx context.Context, c *client.Client) error {
				if err := c.Reboot(ctx, opts...); err != nil {
					return fmt.Errorf("error executing reboot: %s", err)
				}

				return nil
			})
		}

		postCheckFn := func(ctx context.Context, c *client.Client) error {
			_, err := c.Disks(ctx)

			return err
		}

		err := newActionTracker(machineReadyEventFn, rebootGetActorID, postCheckFn, rebootCmdFlags.debug).run()
		if err != nil {
			os.Exit(1)
		}

		return nil
	},
}

func rebootGetActorID(ctx context.Context, c *client.Client) (string, error) {
	resp, err := c.RebootWithResponse(ctx)
	if err != nil {
		return "", err
	}

	if len(resp.GetMessages()) == 0 {
		return "", fmt.Errorf("no messages returned from action run")
	}

	return resp.GetMessages()[0].GetActorId(), nil
}

func init() {
	rebootCmd.Flags().StringVarP(&rebootCmdFlags.mode, "mode", "m", "default", "select the reboot mode: \"default\", \"powercycle\" (skips kexec)")
	rebootCmd.Flags().BoolVar(&rebootCmdFlags.noWait, "no-wait", false, "do not wait for the operation to complete, return immediately. always set to false when --debug is set")
	rebootCmd.Flags().BoolVar(&rebootCmdFlags.debug, "debug", false, "debug operation from kernel logs. --no-wait is set to false when this flag is set")
	addCommand(rebootCmd)
}
