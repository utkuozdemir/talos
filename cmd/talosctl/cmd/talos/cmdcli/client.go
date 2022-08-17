// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package cmdcli

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/talos-systems/crypto/x509"
	"google.golang.org/grpc"

	"github.com/talos-systems/talos/pkg/cli"
	"github.com/talos-systems/talos/pkg/machinery/client"
	clientconfig "github.com/talos-systems/talos/pkg/machinery/client/config"
)

// Context is a context for the Talos command line client.
type Context struct {
	Talosconfig string
	CmdContext  string
	Cluster     string
	Nodes       []string
	Endpoints   []string
}

// NewContext returns a new context with the provided arguments.
func NewContext(talosconfig, cmdContext, cluster string, nodes, endpoints []string) *Context {
	return &Context{
		Talosconfig: talosconfig,
		CmdContext:  cmdContext,
		Cluster:     cluster,
		Nodes:       nodes,
		Endpoints:   endpoints,
	}
}

// WithClientNoNodes wraps common code to initialize Talos client and provide cancellable context.
//
// WithClientNoNodes doesn't set any node information on the request context.
func (c *Context) WithClientNoNodes(action func(context.Context, *client.Client) error, dialOptions ...grpc.DialOption) error {
	return cli.WithContext(
		context.Background(), func(ctx context.Context) error {
			cfg, err := clientconfig.Open(c.Talosconfig)
			if err != nil {
				return fmt.Errorf("failed to open config file %q: %w", c.Talosconfig, err)
			}

			opts := []client.OptionFunc{
				client.WithConfig(cfg),
				client.WithGRPCDialOptions(dialOptions...),
			}

			if c.CmdContext != "" {
				opts = append(opts, client.WithContextName(c.CmdContext))
			}

			if len(c.Endpoints) > 0 {
				// override endpoints from command-line flags
				opts = append(opts, client.WithEndpoints(c.Endpoints...))
			}

			if c.Cluster != "" {
				opts = append(opts, client.WithCluster(c.Cluster))
			}

			c, err := client.New(ctx, opts...)
			if err != nil {
				return fmt.Errorf("error constructing client: %w", err)
			}
			//nolint:errcheck
			defer c.Close()

			return action(ctx, c)
		},
	)
}

// ErrConfigContext is returned when config context cannot be resolved.
var ErrConfigContext = fmt.Errorf("failed to resolve config context")

// WithClient builds upon WithClientNoNodes to provide set of nodes on request context based on config & flags.
func (c *Context) WithClient(action func(context.Context, *client.Client) error, dialOptions ...grpc.DialOption) error {
	return c.WithClientNoNodes(
		func(ctx context.Context, cli *client.Client) error {
			if len(c.Nodes) < 1 {
				configContext := cli.GetConfigContext()
				if configContext == nil {
					return ErrConfigContext
				}

				c.Nodes = configContext.Nodes
			}

			if len(c.Nodes) < 1 {
				return fmt.Errorf("nodes are not set for the command: please use `--nodes` flag or configuration file to set the nodes to run the command against")
			}

			ctx = client.WithNodes(ctx, c.Nodes...)

			return action(ctx, cli)
		},
		dialOptions...,
	)
}

// WithClientMaintenance wraps common code to initialize Talos client in maintenance (insecure mode).
func (c *Context) WithClientMaintenance(enforceFingerprints []string, action func(context.Context, *client.Client) error) error {
	return cli.WithContext(
		context.Background(), func(ctx context.Context) error {
			tlsConfig := &tls.Config{
				InsecureSkipVerify: true,
			}

			if len(enforceFingerprints) > 0 {
				fingerprints := make([]x509.Fingerprint, len(enforceFingerprints))

				for i, stringFingerprint := range enforceFingerprints {
					var err error

					fingerprints[i], err = x509.ParseFingerprint(stringFingerprint)
					if err != nil {
						return fmt.Errorf("error parsing certificate fingerprint %q: %v", stringFingerprint, err)
					}
				}

				tlsConfig.VerifyConnection = x509.MatchSPKIFingerprints(fingerprints...)
			}

			c, err := client.New(ctx, client.WithTLSConfig(tlsConfig), client.WithEndpoints(c.Nodes...))
			if err != nil {
				return err
			}

			//nolint:errcheck
			defer c.Close()

			return action(ctx, c)
		},
	)
}
