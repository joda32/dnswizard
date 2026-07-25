// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

// Package records holds the fake-record store: parsing record definitions into
// wire-format RRs and matching query names against wildcard patterns.
package records

import (
	"fmt"
	"net"
	"strings"

	"github.com/miekg/dns"
)

// DefaultTTL is used for records that do not carry an explicit TTL.
const DefaultTTL uint32 = 60

// Record is a single user-supplied fake answer.
//
// Name is a match pattern, not necessarily a concrete name: it may contain "*"
// labels. Value is the record's RDATA in a friendly form (see Normalise) rather
// than strict zone-file syntax.
type Record struct {
	Name  string // lowercase pattern, no trailing dot
	Type  uint16 // dns.TypeA and friends
	Value string
	TTL   uint32 // 0 means "use the store default"
}

// String renders the record the way it would appear in a config file.
func (r Record) String() string {
	return fmt.Sprintf("%s %s %s", r.Name, dns.TypeToString[r.Type], r.Value)
}

// New builds a Record from its textual parts, validating that the type is known
// and that the value can actually be turned into an RR.
func New(name, rtype, value string, ttl uint32) (Record, error) {
	qtype, ok := ParseType(rtype)
	if !ok {
		return Record{}, fmt.Errorf("unsupported record type %q", rtype)
	}

	name = NormaliseName(name)
	if name == "" {
		return Record{}, fmt.Errorf("record name is empty")
	}

	r := Record{Name: name, Type: qtype, Value: strings.TrimSpace(value), TTL: ttl}
	if r.Value == "" {
		return Record{}, fmt.Errorf("record %s %s has an empty value", name, rtype)
	}

	// Build once up front so configuration errors surface at load time rather
	// than in the middle of answering a query. Wildcards are not valid owner
	// names for dns.NewRR, so validate against a placeholder.
	if _, err := r.RR("validate.invalid", DefaultTTL); err != nil {
		return Record{}, err
	}
	return r, nil
}

// RR renders the record as an answer for the given query name. Wildcard
// patterns answer under the name that was actually queried, which is what a
// resolver expects to see.
func (r Record) RR(qname string, defaultTTL uint32) (dns.RR, error) {
	ttl := r.TTL
	if ttl == 0 {
		ttl = defaultTTL
	}
	if ttl == 0 {
		ttl = DefaultTTL
	}

	rdata, err := Normalise(r.Type, r.Value)
	if err != nil {
		return nil, fmt.Errorf("%s record for %s: %w", dns.TypeToString[r.Type], r.Name, err)
	}

	line := fmt.Sprintf("%s %d IN %s %s", dns.Fqdn(qname), ttl, dns.TypeToString[r.Type], rdata)
	rr, err := dns.NewRR(line)
	if err != nil {
		return nil, fmt.Errorf("%s record for %s: cannot parse %q: %w",
			dns.TypeToString[r.Type], r.Name, r.Value, err)
	}
	return rr, nil
}

// ParseType resolves a record type name such as "A" or "aaaa". "ANY" and "*"
// both map to dns.TypeANY.
func ParseType(s string) (uint16, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "*" || s == "ANY" {
		return dns.TypeANY, true
	}
	t, ok := dns.StringToType[s]
	return t, ok
}

// TypeName is the printable name of a query type, falling back to TYPExx.
func TypeName(t uint16) string {
	if s, ok := dns.TypeToString[t]; ok {
		return s
	}
	return fmt.Sprintf("TYPE%d", t)
}

// NormaliseName lowercases a name or pattern and strips the trailing dot, so
// patterns compare consistently regardless of how they were written.
func NormaliseName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for strings.HasSuffix(name, ".") && name != "." {
		name = strings.TrimSuffix(name, ".")
	}
	if name == "." {
		return ""
	}
	return name
}

// Normalise converts a friendly RDATA value into strict zone-file syntax.
//
// dnswizard accepts shorthand that a zone file would reject — a bare hostname
// for MX, an unquoted TXT string, unquoted NAPTR fields, relative target names.
// Anything not special-cased is passed through untouched, so record types this
// package has never heard of still work as long as the value is valid zone
// syntax.
func Normalise(qtype uint16, value string) (string, error) {
	value = strings.TrimSpace(value)

	switch qtype {
	case dns.TypeA:
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() == nil {
			return "", fmt.Errorf("%q is not an IPv4 address", value)
		}
		return ip.To4().String(), nil

	case dns.TypeAAAA:
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() != nil {
			return "", fmt.Errorf("%q is not an IPv6 address", value)
		}
		return ip.String(), nil

	case dns.TypeCNAME, dns.TypeNS, dns.TypePTR, dns.TypeDNAME:
		if strings.ContainsAny(value, " \t") {
			return "", fmt.Errorf("%q should be a single hostname", value)
		}
		return dns.Fqdn(value), nil

	case dns.TypeMX:
		// Accept either "mail.example.com" or "10 mail.example.com".
		fields := strings.Fields(value)
		switch len(fields) {
		case 1:
			return "10 " + dns.Fqdn(fields[0]), nil
		case 2:
			return fields[0] + " " + dns.Fqdn(fields[1]), nil
		default:
			return "", fmt.Errorf("want \"[preference] host\", got %q", value)
		}

	case dns.TypeTXT:
		return quoteTXT(value), nil

	case dns.TypeSOA:
		// mname rname serial refresh retry expire minimum
		fields := strings.Fields(value)
		if len(fields) != 7 {
			return "", fmt.Errorf("want \"mname rname serial refresh retry expire minimum\", got %q", value)
		}
		fields[0] = dns.Fqdn(fields[0])
		fields[1] = dns.Fqdn(fields[1])
		return strings.Join(fields, " "), nil

	case dns.TypeSRV:
		// priority weight port target
		fields := strings.Fields(value)
		if len(fields) != 4 {
			return "", fmt.Errorf("want \"priority weight port target\", got %q", value)
		}
		fields[3] = dns.Fqdn(fields[3])
		return strings.Join(fields, " "), nil

	case dns.TypeNAPTR:
		// order preference flags service regexp replacement
		fields := strings.Fields(value)
		if len(fields) != 6 {
			return "", fmt.Errorf("want \"order preference flags service regexp replacement\", got %q", value)
		}
		for i := 2; i <= 4; i++ {
			fields[i] = quoteField(fields[i])
		}
		fields[5] = dns.Fqdn(fields[5])
		return strings.Join(fields, " "), nil

	case dns.TypeRRSIG:
		// covered algorithm labels orig_ttl sig_exp sig_inc key_tag signer sig
		fields := strings.Fields(value)
		if len(fields) < 9 {
			return "", fmt.Errorf("want 9 fields \"covered alg labels ttl expiry inception keytag signer signature\", got %q", value)
		}
		fields[7] = dns.Fqdn(fields[7])
		return strings.Join(fields, " "), nil

	case dns.TypeCAA:
		// flag tag value — quote the value if the user left it bare.
		fields := strings.SplitN(value, " ", 3)
		if len(fields) != 3 {
			return "", fmt.Errorf("want \"flag tag value\", got %q", value)
		}
		fields[2] = quoteField(strings.TrimSpace(fields[2]))
		return strings.Join(fields, " "), nil
	}

	return value, nil
}

// quoteTXT splits a string into <=255 byte character-strings and quotes each,
// which is what the wire format requires for anything longer than one chunk.
func quoteTXT(value string) string {
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) && len(value) >= 2 {
		return value // already in zone syntax; trust the user
	}

	const maxChunk = 255
	var chunks []string
	for len(value) > maxChunk {
		chunks = append(chunks, quoteField(value[:maxChunk]))
		value = value[maxChunk:]
	}
	chunks = append(chunks, quoteField(value))
	return strings.Join(chunks, " ")
}

func quoteField(s string) string {
	if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) && len(s) >= 2 {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
