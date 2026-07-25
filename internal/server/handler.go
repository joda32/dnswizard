// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

package server

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/joda32/dnswizard/internal/config"
	"github.com/joda32/dnswizard/internal/records"
)

// ServeDNS implements dns.Handler.
func (s *Server) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	s.stats.Queries.Add(1)

	client := clientIP(w)
	rt := s.current.Load()

	// Anything that is not a plain single-question query gets a protocol-level
	// brush-off rather than being forwarded.
	if req.Opcode != dns.OpcodeQuery || len(req.Question) != 1 {
		s.stats.Failures.Add(1)
		s.log.Debug("unsupported request", "client", client, "opcode", dns.OpcodeToString[req.Opcode],
			"questions", len(req.Question))
		s.respond(w, req, refuse(req, dns.RcodeNotImplemented))
		return
	}

	q := req.Question[0]
	qname := records.NormaliseName(q.Name)
	qtype := records.TypeName(q.Qtype)
	log := s.log.With("client", client, "name", q.Name, "type", qtype)

	if q.Qclass != dns.ClassINET && q.Qclass != dns.ClassANY {
		log.Debug("unsupported class", "class", dns.ClassToString[q.Qclass])
		s.respond(w, req, refuse(req, dns.RcodeRefused))
		return
	}

	if rt.filter.ShouldFake(qname) {
		if answer := s.cook(rt, qname, q.Qtype); len(answer) > 0 {
			s.stats.Cooked.Add(1)
			log.Info("cooked", "answer", summarise(answer))

			resp := reply(req)
			resp.Answer = answer

			// A dangling local CNAME still needs its target resolved, which
			// only the upstream can do.
			if target := danglingTarget(answer, q.Qtype); target != "" && rt.fallback == config.FallbackProxy {
				s.appendUpstream(resp, target, q.Qtype, log)
			}
			s.respond(w, req, resp)
			return
		}

		// The name is ours but not for this type. Answering NODATA keeps
		// internal names from leaking to a public resolver.
		if rt.noDataForKnownNames && rt.store.Knows(qname) {
			s.stats.Cooked.Add(1)
			log.Info("nodata", "reason", "name is local, no record of this type")
			s.respond(w, req, reply(req))
			return
		}
	}

	switch rt.fallback {
	case config.FallbackNXDomain:
		s.stats.Blocked.Add(1)
		log.Info("blocked", "rcode", "NXDOMAIN")
		s.respond(w, req, refuse(req, dns.RcodeNameError))
	case config.FallbackRefused:
		s.stats.Blocked.Add(1)
		log.Info("blocked", "rcode", "REFUSED")
		s.respond(w, req, refuse(req, dns.RcodeRefused))
	case config.FallbackEmpty:
		s.stats.Blocked.Add(1)
		log.Info("blocked", "rcode", "NOERROR", "answers", 0)
		s.respond(w, req, reply(req))
	default:
		s.proxy(w, req, log)
	}
}

// cook builds the local answer for a query, or nil if there is none.
func (s *Server) cook(rt *runtime, qname string, qtype uint16) []dns.RR {
	ttl := rt.store.DefaultTTL()

	if qtype == dns.TypeANY {
		// An ANY query is answered with every record known for the name.
		var answer []dns.RR
		for _, t := range rt.store.TypesFor(qname) {
			for _, rec := range rt.store.Lookup(qname, t) {
				if rr, err := rec.RR(qname, ttl); err == nil {
					answer = append(answer, rr)
				} else {
					s.log.Warn("skipping unbuildable record", "record", rec.String(), "error", err)
				}
			}
		}
		return answer
	}

	// Walk the local records, following any CNAMEs we defined ourselves.
	// Whatever is left dangling gets resolved upstream by the caller.
	var (
		answer []dns.RR
		name   = qname
		chase  = rt.chaseCNAME && qtype != dns.TypeCNAME &&
			(qtype == dns.TypeA || qtype == dns.TypeAAAA)
	)

	for depth := 0; depth < maxCNAMEDepth; depth++ {
		direct, directScore := rt.store.LookupScored(name, qtype)

		aliases, aliasScore := []records.Record(nil), -1
		if chase {
			aliases, aliasScore = rt.store.LookupScored(name, dns.TypeCNAME)
		}

		// An explicit CNAME outranks a broader wildcard of the queried type,
		// the same way a real zone would: no other data can live alongside a
		// CNAME at that exact name.
		if len(aliases) > 0 && (len(direct) == 0 || aliasScore > directScore) {
			rr, err := aliases[0].RR(name, ttl)
			if err != nil {
				s.log.Warn("skipping unbuildable record", "record", aliases[0].String(), "error", err)
				return answer
			}
			cname, ok := rr.(*dns.CNAME)
			if !ok {
				return answer
			}
			answer = append(answer, cname)
			name = records.NormaliseName(cname.Target)
			continue
		}

		if len(direct) > 0 {
			return append(answer, s.build(direct, name, ttl)...)
		}
		return answer
	}

	s.log.Warn("CNAME chain too long, giving up", "name", qname)
	return answer
}

func (s *Server) build(recs []records.Record, owner string, ttl uint32) []dns.RR {
	out := make([]dns.RR, 0, len(recs))
	for _, rec := range recs {
		rr, err := rec.RR(owner, ttl)
		if err != nil {
			s.log.Warn("skipping unbuildable record", "record", rec.String(), "error", err)
			continue
		}
		out = append(out, rr)
	}
	return out
}

// danglingTarget reports the CNAME target that still needs resolving, i.e. the
// answer ends in a CNAME with nothing after it for the queried type.
func danglingTarget(answer []dns.RR, qtype uint16) string {
	if len(answer) == 0 || (qtype != dns.TypeA && qtype != dns.TypeAAAA) {
		return ""
	}
	last, ok := answer[len(answer)-1].(*dns.CNAME)
	if !ok {
		return ""
	}
	return last.Target
}

// appendUpstream resolves target and appends whatever it finds, so a client
// following a lab CNAME out to a real name gets a complete answer in one go.
func (s *Server) appendUpstream(resp *dns.Msg, target string, qtype uint16, log *slog.Logger) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(target), qtype)
	m.RecursionDesired = true

	result, err := s.resolver.Exchange(context.Background(), m)
	if err != nil {
		log.Warn("could not resolve CNAME target", "target", target, "error", err)
		return
	}
	resp.Answer = append(resp.Answer, result.Msg.Answer...)
	log.Debug("followed CNAME upstream", "target", target, "upstream", result.Server.String(),
		"answers", len(result.Msg.Answer))
}

// proxy forwards the query untouched and relays the reply.
func (s *Server) proxy(w dns.ResponseWriter, req *dns.Msg, log *slog.Logger) {
	// Forward a copy so the upstream cannot see our own EDNS quirks and so the
	// original stays intact for building an error reply.
	out := req.Copy()
	out.Id = dns.Id()

	result, err := s.resolver.Exchange(context.Background(), out)
	if err != nil {
		s.stats.Failures.Add(1)
		log.Error("proxy failed", "error", err)
		s.respond(w, req, refuse(req, dns.RcodeServerFailure))
		return
	}

	s.stats.Proxied.Add(1)
	resp := result.Msg
	resp.Id = req.Id
	resp.Question = req.Question

	log.Info("proxied", "upstream", result.Server.String(),
		"rcode", dns.RcodeToString[resp.Rcode], "answers", len(resp.Answer),
		"rtt", result.RTT.Round(100*time.Microsecond))

	s.respond(w, req, resp)
}

// reply builds an authoritative NOERROR skeleton for req.
func reply(req *dns.Msg) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true
	m.RecursionAvailable = true
	return m
}

func refuse(req *dns.Msg, rcode int) *dns.Msg {
	m := new(dns.Msg)
	m.SetRcode(req, rcode)
	m.RecursionAvailable = true
	m.Authoritative = rcode == dns.RcodeNameError
	return m
}

// respond mirrors EDNS0 back to the client, truncates if the answer will not
// fit the client's advertised UDP buffer, and writes.
func (s *Server) respond(w dns.ResponseWriter, req, resp *dns.Msg) {
	if resp == nil {
		return
	}

	udpSize := dns.MinMsgSize
	if opt := req.IsEdns0(); opt != nil {
		if resp.IsEdns0() == nil {
			resp.SetEdns0(opt.UDPSize(), opt.Do())
		}
		if sz := int(opt.UDPSize()); sz > udpSize {
			udpSize = sz
		}
	}

	if _, isUDP := w.LocalAddr().(*net.UDPAddr); isUDP {
		resp.Truncate(udpSize)
	}

	if err := w.WriteMsg(resp); err != nil {
		s.stats.Failures.Add(1)
		s.log.Debug("write failed", "client", clientIP(w), "error", err)
	}
}

func clientIP(w dns.ResponseWriter) string {
	addr := w.RemoteAddr()
	if addr == nil {
		return "unknown"
	}
	if host, _, err := net.SplitHostPort(addr.String()); err == nil {
		return host
	}
	return addr.String()
}

// summarise renders an answer set compactly for the log line.
func summarise(answer []dns.RR) string {
	parts := make([]string, 0, len(answer))
	for _, rr := range answer {
		text := rr.String()
		// Drop the leading "name ttl class type" so only the RDATA shows.
		if fields := strings.SplitN(text, "\t", 5); len(fields) == 5 {
			text = fields[4]
		}
		if len(answer) > 1 {
			text = records.TypeName(rr.Header().Rrtype) + " " + text
		}
		parts = append(parts, strings.TrimSpace(text))
	}
	return strings.Join(parts, ", ")
}
