// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/spf13/cobra"

	"github.com/joda32/dnswizard/internal/records"
	"github.com/joda32/dnswizard/internal/upstream"
)

func newQueryCommand() *cobra.Command {
	var (
		serverAddr string
		timeout    time.Duration
		useTCP     bool
	)

	cmd := &cobra.Command{
		Use:     "query <name> [type]",
		Aliases: []string{"dig", "lookup"},
		Short:   "Send a query to a DNS server (defaults to the local dnswizard)",
		Long: strings.TrimSpace(`
Send a single DNS query and print the reply.

A small stand-in for dig, so a fresh machine can test a dnswizard setup without
installing anything else.`),
		Example: strings.TrimSpace(`
  dnswizard query api.dev.local
  dnswizard query dev.local MX
  dnswizard query example.com A -s 1.1.1.1:53`),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			qtype := dns.TypeA
			if len(args) == 2 {
				t, ok := records.ParseType(args[1])
				if !ok {
					return fmt.Errorf("unknown record type %q", args[1])
				}
				qtype = t
			}

			if useTCP && !strings.Contains(serverAddr, "://") {
				serverAddr = "tcp://" + serverAddr
			}
			resolver, err := upstream.New([]string{serverAddr}, timeout)
			if err != nil {
				return err
			}

			m := new(dns.Msg)
			m.SetQuestion(dns.Fqdn(args[0]), qtype)
			m.RecursionDesired = true
			m.SetEdns0(4096, false)

			result, err := resolver.Exchange(context.Background(), m)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, ";; server: %s  rtt: %s  rcode: %s\n",
				result.Server, result.RTT.Round(100*time.Microsecond), dns.RcodeToString[result.Msg.Rcode])

			if len(result.Msg.Answer) == 0 {
				fmt.Fprintln(out, ";; no answer records")
			}
			for _, rr := range result.Msg.Answer {
				fmt.Fprintln(out, rr.String())
			}
			for _, rr := range result.Msg.Ns {
				fmt.Fprintf(out, ";; authority: %s\n", rr.String())
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&serverAddr, "server", "s", "127.0.0.1:53", "server to query")
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Second, "query timeout")
	cmd.Flags().BoolVar(&useTCP, "tcp", false, "query over TCP")
	return cmd
}
