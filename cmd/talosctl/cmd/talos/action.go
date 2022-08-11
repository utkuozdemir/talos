// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package talos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/talos-systems/go-retry/retry"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"

	"github.com/talos-systems/talos/cmd/talosctl/pkg/talos/helpers"
	"github.com/talos-systems/talos/pkg/circular"
	"github.com/talos-systems/talos/pkg/machinery/api/common"
	machineapi "github.com/talos-systems/talos/pkg/machinery/api/machine"
	"github.com/talos-systems/talos/pkg/machinery/client"
	"github.com/talos-systems/talos/pkg/machinery/generic/maps"
	"github.com/talos-systems/talos/pkg/reporter"
)

type nodeUpdate struct {
	node   string
	update reporter.Update
}

// actionTracker runs the action in the actionFn on the nodes and tracks its progress using the provided expectedEventFn and postCheckFn.
type actionTracker struct {
	expectedEventFn          func(event client.EventResult) bool
	actionFn                 func(ctx context.Context, c *client.Client) (string, error)
	postCheckFn              func(ctx context.Context, c *client.Client) error
	reporter                 *reporter.Reporter
	nodeToLatestStatusUpdate map[string]reporter.Update
	reportCh                 chan nodeUpdate
	retryDuration            time.Duration
	isTerminal               bool
	debug                    bool
}

// nodeActionTracker tracks the actions of a single node.
type nodeActionTracker struct {
	ctx           context.Context //nolint:containedctx
	node          string
	actionTracker *actionTracker
	dmesg         *circular.Buffer
	cli           *client.Client
}

var (
	machineReadyEventFn = func(event client.EventResult) bool {
		machineStatusEvent, ok := event.Event.Payload.(*machineapi.MachineStatusEvent)
		if !ok {
			return false
		}

		return machineStatusEvent.GetStage() == machineapi.MachineStatusEvent_RUNNING &&
			machineStatusEvent.GetStatus().GetReady()
	}

	stopAllServicesEventFn = func(event client.EventResult) bool {
		taskEvent, ok := event.Event.Payload.(*machineapi.TaskEvent)
		if !ok {
			return false
		}

		return taskEvent.GetTask() == "stopAllServices"
	}
)

// newActionTracker creates a new actionTracker.
func newActionTracker(
	expectedEventFn func(event client.EventResult) bool,
	actionFn func(ctx context.Context, c *client.Client) (string, error),
	postCheckFn func(ctx context.Context, c *client.Client) error,
	debug bool,
) *actionTracker {
	return &actionTracker{
		expectedEventFn:          expectedEventFn,
		actionFn:                 actionFn,
		postCheckFn:              postCheckFn,
		nodeToLatestStatusUpdate: make(map[string]reporter.Update, len(Nodes)),
		reporter:                 reporter.New(),
		reportCh:                 make(chan nodeUpdate),
		retryDuration:            5 * time.Minute,
		isTerminal:               isatty.IsTerminal(os.Stderr.Fd()),
		debug:                    debug,
	}
}

// run executes the action on nodes and tracks its progress by watching events with retries.
// After receiving the expected event, if provided, it tracks the progress by running the post check with retries.
//
//nolint:gocyclo
func (a *actionTracker) run() error {
	failedNodesToDmesgs := make(map[string]io.Reader)

	var eg errgroup.Group

	defer func() {
		eg.Wait() //nolint:errcheck

		if a.debug && len(failedNodesToDmesgs) > 0 {
			fmt.Printf("Debug information of nodes %v:\n", maps.Keys(failedNodesToDmesgs))

			for node, dmesgReader := range failedNodesToDmesgs {
				debugBytes, err := io.ReadAll(dmesgReader)
				if err != nil {
					fmt.Printf("%v: failed to read debug logs: %v\n", node, err)

					return
				}

				fmt.Println(string(debugBytes))
			}
		}
	}()

	return WithClient(func(ctx context.Context, c *client.Client) error {
		eg.Go(func() error {
			return a.runReporter(ctx)
		})

		var trackEg errgroup.Group

		for _, node := range Nodes {
			node := node

			var (
				dmesg *circular.Buffer
				err   error
			)

			if a.debug {
				dmesg, err = circular.NewBuffer()
				dmesg.GetReader()
				if err != nil {
					return err
				}
			}

			tracker := nodeActionTracker{
				ctx:           client.WithNode(ctx, node),
				node:          node,
				actionTracker: a,
				dmesg:         dmesg,
				cli:           c,
			}

			if a.debug {
				eg.Go(tracker.tailDebugLogs)
			}

			trackEg.Go(func() error {
				if trackErr := tracker.run(); trackErr != nil {
					if a.debug {
						failedNodesToDmesgs[node] = dmesg.GetReader()
					}

					tracker.update(reporter.Update{
						Message: trackErr.Error(),
						Status:  reporter.StatusError,
					})
				}

				return err
			})
		}

		return trackEg.Wait()
	}, grpc.WithConnectParams(grpc.ConnectParams{
		// disable grpc backoff
		Backoff:           backoff.Config{},
		MinConnectTimeout: 20 * time.Second,
	}))
}

// runReporter starts the (colored) stderr reporter.
func (a *actionTracker) runReporter(ctx context.Context) error {
	var update nodeUpdate

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update = <-a.reportCh:
		}

		if !a.isTerminal {
			fmt.Printf("%v: %v\n", update.node, update.update.Message)

			continue
		}

		if update.node != "" {
			a.nodeToLatestStatusUpdate[update.node] = update.update
		}

		nodes := maps.Keys(a.nodeToLatestStatusUpdate)
		sort.Strings(nodes)

		combineStatus := func() reporter.Status {
			combined := reporter.StatusSucceeded

			for _, status := range a.nodeToLatestStatusUpdate {
				if status.Status == reporter.StatusError {
					return reporter.StatusError
				}

				if status.Status == reporter.StatusRunning {
					combined = reporter.StatusRunning
				}
			}

			return combined
		}

		messages := make([]string, 0, len(nodes)+1)
		messages = append(messages, fmt.Sprintf("Watching nodes: %v", nodes))

		for _, node := range nodes {
			update := a.nodeToLatestStatusUpdate[node]

			messages = append(messages, fmt.Sprintf("    * %s: %s", node, update.Message))
		}

		combinedMessage := strings.Join(messages, "\n")

		a.reporter.Report(reporter.Update{
			Message: combinedMessage,
			Status:  combineStatus(),
		})
	}
}

// tailDebugLogs starts tailing the dmesg of the node.
func (a *nodeActionTracker) tailDebugLogs() error {
	return retry.Constant(a.actionTracker.retryDuration).Retry(func() error {
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

func (a *nodeActionTracker) run() error {
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

	actorID, err = a.actionTracker.actionFn(a.ctx, a.cli)
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

	if a.actionTracker.postCheckFn == nil {
		return nil
	}

	return a.runPostCheckWithRetry()
}

func (a *nodeActionTracker) update(update reporter.Update) {
	select {
	case a.actionTracker.reportCh <- nodeUpdate{
		node:   a.node,
		update: update,
	}:
	case <-a.ctx.Done():
	}
}

func (a *nodeActionTracker) trackEventsWithRetry(actorIDCh chan string) error {
	var (
		tailEvents     int32
		actorID        string
		waitForActorID = true
	)

	return retry.Constant(a.actionTracker.retryDuration).Retry(func() error {
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

func (a *nodeActionTracker) runPostCheckWithRetry() error {
	return retry.Constant(a.actionTracker.retryDuration).Retry(func() error {
		// retryable function
		err := func() error {
			err := a.actionTracker.postCheckFn(a.ctx, a.cli)
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

func (a *nodeActionTracker) handleEvents(eventCh chan client.EventResult, actorID string) error {
	for {
		var eventResult client.EventResult

		select {
		case eventResult = <-eventCh:
		case <-a.ctx.Done():
			return a.ctx.Err()
		}

		if a.actionTracker.expectedEventFn(eventResult) {
			status := reporter.StatusSucceeded
			if a.actionTracker.postCheckFn != nil {
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

func (a *nodeActionTracker) handleEvent(event client.Event) error {
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
