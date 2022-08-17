// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package action

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/talos-systems/go-retry/retry"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"

	"github.com/talos-systems/talos/cmd/talosctl/pkg/talos/helpers"
	"github.com/talos-systems/talos/pkg/circular"
	"github.com/talos-systems/talos/pkg/machinery/api/common"
	machineapi "github.com/talos-systems/talos/pkg/machinery/api/machine"
	"github.com/talos-systems/talos/pkg/machinery/client"
	"github.com/talos-systems/talos/pkg/reporter"
)

// nodeTracker tracks the actions of a single node.
type nodeTracker struct {
	ctx     context.Context //nolint:containedctx
	node    string
	tracker *Tracker
	dmesg   *circular.Buffer
	cli     *client.Client
}

// tailDebugLogs starts tailing the dmesg of the node.
func (a *nodeTracker) tailDebugLogs() error {
	return retry.Constant(a.tracker.retryDuration).Retry(func() error {
		err := func() error {
			stream, err := a.cli.Dmesg(a.ctx, true, true)
			if err != nil {
				return err
			}

			return helpers.ReadGRPCStream(stream, func(data *common.Data, _ string, _ bool) error {
				_, err := a.dmesg.Write([]byte(fmt.Sprintf("%s: %s", a.node, data.GetBytes())))

				return err
			})
		}()
		if err == nil {
			return nil
		}

		if errors.Is(err, context.Canceled) {
			return nil
		}

		if strings.Contains(err.Error(), "file already closed") {
			return retry.ExpectedError(err)
		}

		statusCode := client.StatusCode(err)
		if statusCode == codes.Unavailable {
			return retry.ExpectedError(err)
		}

		return err
	})
}

func (a *nodeTracker) run() error {
	var (
		actorIDCh chan string
		nodeEg    errgroup.Group
	)

	actorIDCh = make(chan string)

	nodeEg.Go(func() error {
		return a.trackEventsWithRetry(actorIDCh)
	})

	var (
		actorID string
		err     error
	)

	actorID, err = a.tracker.actionFn(a.ctx, a.cli)
	if err != nil {
		return err
	}

	select {
	case actorIDCh <- actorID:
	case <-a.ctx.Done():
		return a.ctx.Err()
	}

	err = nodeEg.Wait()
	if err != nil {
		return err
	}

	if a.tracker.postCheckFn == nil {
		return nil
	}

	return a.runPostCheckWithRetry()
}

func (a *nodeTracker) update(update reporter.Update) {
	select {
	case a.tracker.reportCh <- nodeUpdate{
		node:   a.node,
		update: update,
	}:
	case <-a.ctx.Done():
	}
}

func (a *nodeTracker) trackEventsWithRetry(actorIDCh chan string) error {
	var (
		tailEvents     int32
		actorID        string
		waitForActorID = true
	)

	return retry.Constant(a.tracker.retryDuration).Retry(func() error {
		// retryable function
		err := func() error {
			eventCh := make(chan client.EventResult)
			err := a.cli.EventsWatchV2(a.ctx, eventCh, client.WithTailEvents(tailEvents))
			if err != nil {
				return err
			}

			if waitForActorID {
				a.update(reporter.Update{
					Message: "Waiting for actor ID",
					Status:  reporter.StatusRunning,
				})

				select {
				case actorID = <-actorIDCh:
				case <-a.ctx.Done():
					return a.ctx.Err()
				}

				a.update(reporter.Update{
					Message: fmt.Sprintf("Actor ID: %v", actorID),
					Status:  reporter.StatusRunning,
				})

				waitForActorID = false
			}

			return a.handleEvents(eventCh, actorID)
		}()

		// handle retryable errors

		if errors.Is(err, context.Canceled) {
			return nil
		}

		statusCode := client.StatusCode(err)
		if statusCode == codes.Unavailable {
			a.update(reporter.Update{
				Message: "Unavailable, retrying...",
				Status:  reporter.StatusError,
			})

			tailEvents = -1
			actorID = ""

			return retry.ExpectedError(err)
		}

		return err
	})
}

func (a *nodeTracker) runPostCheckWithRetry() error {
	return retry.Constant(a.tracker.retryDuration).Retry(func() error {
		// retryable function
		err := func() error {
			err := a.tracker.postCheckFn(a.ctx, a.cli)
			if err != nil {
				return err
			}

			a.update(reporter.Update{
				Message: "Post check passed",
				Status:  reporter.StatusSucceeded,
			})

			return nil
		}()

		// handle retryable errors

		if errors.Is(err, context.Canceled) {
			return nil
		}

		statusCode := client.StatusCode(err)
		if statusCode == codes.Unavailable {
			a.update(reporter.Update{
				Message: "Unavailable, retrying...",
				Status:  reporter.StatusError,
			})

			return retry.ExpectedError(err)
		}

		return err
	})
}

func (a *nodeTracker) handleEvents(eventCh chan client.EventResult, actorID string) error {
	for {
		var eventResult client.EventResult

		select {
		case eventResult = <-eventCh:
		case <-a.ctx.Done():
			return a.ctx.Err()
		}

		if a.tracker.expectedEventFn(eventResult) {
			status := reporter.StatusSucceeded
			if a.tracker.postCheckFn != nil {
				status = reporter.StatusRunning
			}

			a.update(reporter.Update{
				Message: "Events check condition met",
				Status:  status,
			})

			return nil
		}

		if eventResult.Error != nil {
			a.update(reporter.Update{
				Message: fmt.Sprintf("Error: %v", eventResult.Error),
				Status:  reporter.StatusError,
			})

			return eventResult.Error
		}

		if eventResult.Event.ActorID == actorID {
			err := a.handleEvent(eventResult.Event)
			if err != nil {
				return err
			}
		}
	}
}

func (a *nodeTracker) handleEvent(event client.Event) error {
	switch msg := event.Payload.(type) {
	case *machineapi.PhaseEvent:
		a.update(reporter.Update{
			Message: fmt.Sprintf("PhaseEvent phase: %s action: %v", msg.GetPhase(), msg.GetAction()),
			Status:  reporter.StatusRunning,
		})

	case *machineapi.TaskEvent:
		a.update(reporter.Update{
			Message: fmt.Sprintf("TaskEvent task: %s action: %v", msg.GetTask(), msg.GetAction()),
			Status:  reporter.StatusRunning,
		})

		if msg.GetTask() == "stopAllServices" {
			return retry.ExpectedError(fmt.Errorf("stopAllServices task completed"))
		}

	case *machineapi.SequenceEvent:
		errStr := ""
		if msg.GetError().GetMessage() != "" {
			errStr = fmt.Sprintf(
				" error: [code: %v message: %v]",
				msg.GetError().GetMessage(),
				msg.GetError().GetCode(),
			)
		}

		a.update(reporter.Update{
			Message: fmt.Sprintf("SequenceEvent sequence: %s action: %v%v", msg.GetSequence(), msg.GetAction(), errStr),
			Status:  reporter.StatusRunning,
		})

	case *machineapi.MachineStatusEvent:
		a.update(reporter.Update{
			Message: fmt.Sprintf("MachineStatusEvent: stage: %v ready: %v unmetCond: %v", msg.GetStage(), msg.GetStatus().GetReady(), msg.GetStatus().GetUnmetConditions()),
			Status:  reporter.StatusRunning,
		})

	case *machineapi.ServiceStateEvent:
		a.update(reporter.Update{
			Message: fmt.Sprintf("ServiceStateEvent service: %v message: %v healthy: %v", msg.GetService(), msg.GetMessage(), msg.GetHealth().GetHealthy()),
			Status:  reporter.StatusRunning,
		})

	case *machineapi.AddressEvent:
		a.update(reporter.Update{
			Message: fmt.Sprintf("AddressEvent hostname: %v addresses: %v", msg.GetHostname(), msg.GetAddresses()),
			Status:  reporter.StatusRunning,
		})

	default:
		a.update(reporter.Update{
			Message: fmt.Sprintf("Unknown Event: %v: %+v", reflect.TypeOf(msg), msg),
			Status:  reporter.StatusRunning,
		})
	}

	return nil
}
