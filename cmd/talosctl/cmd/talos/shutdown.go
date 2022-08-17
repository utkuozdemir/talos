// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package talos

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/talos-systems/talos/cmd/talosctl/cmd/talos/action"
	"github.com/talos-systems/talos/pkg/machinery/client"
)

var shutdownCmdFlags struct {
	force  bool
	noWait bool
	debug  bool
}

// shutdownCmd represents the shutdown command.
var shutdownCmd = &cobra.Command{
	Use:   "shutdown",
	Short: "Shutdown a node",
	Long:  ``,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if shutdownCmdFlags.debug {
			shutdownCmdFlags.noWait = false
		}

		opts := []client.ShutdownOption{
			client.WithShutdownForce(shutdownCmdFlags.force),
		}

		if shutdownCmdFlags.noWait {
			return WithClient(func(ctx context.Context, c *client.Client) error {
				if err := c.Shutdown(ctx, opts...); err != nil {
					return fmt.Errorf("error executing shutdown: %s", err)
				}

				return nil
			})
		}

		err := action.NewTracker(buildCmdCliContext(), action.StopAllServicesEventFn, shutdownGetActorID, nil, shutdownCmdFlags.debug).Run()
		if err != nil {
			os.Exit(1)
		}

		return err
	},
}

func shutdownGetActorID(ctx context.Context, c *client.Client) (string, error) {
	resp, err := c.ShutdownWithResponse(ctx, client.WithShutdownForce(shutdownCmdFlags.force))
	if err != nil {
		return "", err
	}

	if len(resp.GetMessages()) == 0 {
		return "", fmt.Errorf("no messages returned from action run")
	}

	return resp.GetMessages()[0].GetActorId(), nil
}

func init() {
	shutdownCmd.Flags().BoolVar(&shutdownCmdFlags.force, "force", false, "if true, force a node to shutdown without a cordon/drain")
	shutdownCmd.Flags().BoolVar(&shutdownCmdFlags.noWait, "no-wait", false, "do not wait for the operation to complete, return immediately. always set to false when --debug is set")
	shutdownCmd.Flags().BoolVar(&shutdownCmdFlags.debug, "debug", false, "debug operation from kernel logs. --no-wait is set to false when this flag is set")
	addCommand(shutdownCmd)
}
