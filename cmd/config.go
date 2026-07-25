// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/joda32/dnswizard/internal/config"
	"github.com/joda32/dnswizard/internal/records"
)

//nolint:lll // the sample config is deliberately verbose
const sampleConfig = `# dnswizard configuration
#
# Names are matched most-specific-first: an exact name beats "*.dev.local",
# which beats "*". A leading "*" matches one or more labels, so
# "*.dev.local" covers both "api.dev.local" and "v2.api.dev.local".

listen:
  - 127.0.0.1:53          # bare address binds both UDP and TCP
  # - udp://0.0.0.0:53    # or pin a single transport

upstream:                 # tried in order, with failover
  - 1.1.1.1:53
  - 8.8.8.8:53
  # - tls://1.1.1.1:853#cloudflare-dns.com   # DNS over TLS

ttl: 60
timeout: 3s

# What to do with queries that match no record:
#   proxy (forward upstream) | nxdomain | refused | empty
fallback: proxy

# Answer NODATA instead of forwarding when a name is known locally but has no
# record of the queried type. Keeps internal names off the public internet.
nodata_for_known_names: true

# Follow locally-defined CNAMEs when answering A/AAAA queries.
chase_cname: true

# Shorthand for address records: A or AAAA is chosen from the address family.
hosts:
  "*.dev.local": 127.0.0.1
  "api.dev.local":
    - 10.0.0.10
    - 10.0.0.11
  "ipv6.dev.local": "::1"

# Everything else goes here.
records:
  - name: "dev.local"
    type: MX
    value: mail.dev.local          # preference defaults to 10

  - name: "_sip._tcp.dev.local"
    type: SRV
    value: "0 5 5060 sip.dev.local"

  - name: "dev.local"
    type: TXT
    value: "v=spf1 -all"
    ttl: 300

  - name: "old.dev.local"
    type: CNAME
    value: api.dev.local

# Limit which names may be faked (mutually exclusive):
#   only:   fake just these, proxy everything else
#   except: proxy just these, fake everything else
# only:
#   - "*.dev.local"
`

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Create, check and import configuration files",
	}
	cmd.AddCommand(newConfigInitCommand(), newConfigCheckCommand(), newConfigImportCommand())
	return cmd
}

func newConfigInitCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init [path]",
		Short: "Write a commented starter config",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "dnswizard.yaml"
			if len(args) == 1 {
				path = args[0]
			}

			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", path)
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}

			if err := os.WriteFile(path, []byte(sampleConfig), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\nedit it, then run: dnswizard serve -c %s\n", path, path)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite an existing file")
	return cmd
}

func newConfigCheckCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "check [path]",
		Aliases: []string{"validate", "test"},
		Short:   "Validate a config file and show what it would serve",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "dnswizard.yaml"
			if len(args) == 1 {
				path = args[0]
			}

			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			store, err := cfg.BuildStore()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s: ok\n\n", path)
			fmt.Fprintf(out, "listen    %s\n", strings.Join(cfg.Listen, ", "))
			fmt.Fprintf(out, "upstream  %s\n", strings.Join(cfg.Upstream, ", "))
			fmt.Fprintf(out, "fallback  %s\n", cfg.Fallback)
			fmt.Fprintf(out, "filter    %s\n", cfg.BuildFilter().Describe())
			fmt.Fprintf(out, "records   %d\n", store.Len())

			if store.Len() > 0 {
				fmt.Fprintln(out)
				tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tTYPE\tTTL\tVALUE")
				for _, r := range store.All() {
					ttl := r.TTL
					if ttl == 0 {
						ttl = cfg.TTL
					}
					fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", r.Name, records.TypeName(r.Type), ttl, r.Value)
				}
				_ = tw.Flush()
			}
			return nil
		},
	}
}

func newConfigImportCommand() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "import <file.ini>",
		Short: "Convert a legacy INI record file to dnswizard YAML",
		Long: strings.TrimSpace(`
Convert a legacy INI record file to dnswizard YAML.

The INI format uses a section per record type and one "domain=value" pair per
line:

  [A]
  *.example.com=192.0.2.1

Wildcards carry over as written, but they are matched more strictly here: a
bare "example.com" matches only that exact name, not every subdomain. Add an
explicit "*.example.com" entry if you relied on the looser behaviour.`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.ImportINI(args[0])

			var skipped *config.SkippedSectionsError
			switch {
			case errors.As(err, &skipped):
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", skipped)
			case err != nil:
				return err
			}

			data, err := cfg.Marshal()
			if err != nil {
				return err
			}
			data = append([]byte("# Imported from "+args[0]+"\n"), data...)

			if output == "" || output == "-" {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			if err := os.WriteFile(output, data, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s\n", output)
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "-", "write to this file instead of stdout")
	return cmd
}
