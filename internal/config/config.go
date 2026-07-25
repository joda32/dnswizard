// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

// Package config loads and validates dnswizard's YAML configuration and turns
// it into the runtime objects the server needs.
package config

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/miekg/dns"
	"gopkg.in/yaml.v3"

	"github.com/joda32/dnswizard/internal/records"
)

// Fallback decides what happens to a query with no matching fake record.
type Fallback string

const (
	// FallbackProxy forwards the query to an upstream resolver.
	FallbackProxy Fallback = "proxy"
	// FallbackNXDomain answers NXDOMAIN without contacting anyone.
	FallbackNXDomain Fallback = "nxdomain"
	// FallbackRefused answers REFUSED.
	FallbackRefused Fallback = "refused"
	// FallbackEmpty answers NOERROR with no records.
	FallbackEmpty Fallback = "empty"
)

// Valid reports whether f is a known fallback mode.
func (f Fallback) Valid() bool {
	switch f {
	case FallbackProxy, FallbackNXDomain, FallbackRefused, FallbackEmpty:
		return true
	}
	return false
}

// Config is the on-disk configuration, also used to carry CLI overrides.
type Config struct {
	// Listen addresses, as "host:port". Prefix with "udp://" or "tcp://" to
	// bind only one transport; bare addresses bind both.
	Listen []string `yaml:"listen,omitempty"`
	// Upstream resolvers used for proxied queries, tried in order.
	Upstream []string `yaml:"upstream,omitempty"`
	// TTL applied to records that do not set their own.
	TTL uint32 `yaml:"ttl,omitempty"`
	// Timeout for a single upstream exchange, e.g. "3s".
	Timeout Duration `yaml:"timeout,omitempty"`
	// Fallback behaviour for unmatched queries.
	Fallback Fallback `yaml:"fallback,omitempty"`

	// Only fakes just these name patterns and proxies everything else.
	Only []string `yaml:"only,omitempty"`
	// Except proxies these name patterns and fakes everything else.
	Except []string `yaml:"except,omitempty"`

	// Hosts is the shorthand form: name -> address(es), with A or AAAA picked
	// automatically from the address family.
	Hosts map[string]StringList `yaml:"hosts,omitempty"`
	// Records is the long form, for every other record type.
	Records []RecordSpec `yaml:"records,omitempty"`

	// NoDataForKnownNames answers NODATA rather than proxying when a name has
	// records of some other type. Keeps internal names off the wire.
	NoDataForKnownNames *bool `yaml:"nodata_for_known_names,omitempty"`
	// ChaseCNAME appends the CNAME target's addresses to A/AAAA answers.
	ChaseCNAME *bool `yaml:"chase_cname,omitempty"`

	Log LogSpec `yaml:"log,omitempty"`

	// Path records where this config was loaded from. Not serialised.
	Path string `yaml:"-"`
}

// LogSpec configures logging from the config file.
type LogSpec struct {
	Level  string `yaml:"level,omitempty"`
	Format string `yaml:"format,omitempty"`
	File   string `yaml:"file,omitempty"`
}

// RecordSpec is one record definition in the config file. Either Value or
// Values may be set; both forms accept a bare string or a list.
type RecordSpec struct {
	Name   string     `yaml:"name"`
	Type   string     `yaml:"type"`
	Value  StringList `yaml:"value,omitempty"`
	Values StringList `yaml:"values,omitempty"`
	TTL    uint32     `yaml:"ttl,omitempty"`
}

// Duration is a time.Duration that reads and writes as a string such as "3s"
// rather than as a raw nanosecond count.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("line %d: want a duration string such as \"3s\"", node.Line)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// StringList accepts either a single scalar or a sequence in YAML, so both
// "value: 10.0.0.1" and "value: [10.0.0.1, 10.0.0.2]" work.
type StringList []string

// MarshalYAML writes a single-element list back out as a plain scalar, which
// keeps generated configs readable.
func (s StringList) MarshalYAML() (any, error) {
	if len(s) == 1 {
		return s[0], nil
	}
	return []string(s), nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (s *StringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var v string
		if err := node.Decode(&v); err != nil {
			return err
		}
		*s = StringList{v}
		return nil
	case yaml.SequenceNode:
		var v []string
		if err := node.Decode(&v); err != nil {
			return err
		}
		*s = v
		return nil
	default:
		return fmt.Errorf("line %d: want a string or a list of strings", node.Line)
	}
}

// Default returns the configuration used when nothing else is specified.
func Default() *Config {
	nodata, chase := true, true
	return &Config{
		Listen:              []string{"127.0.0.1:53"},
		Upstream:            []string{"1.1.1.1:53", "8.8.8.8:53"},
		TTL:                 records.DefaultTTL,
		Timeout:             Duration(3 * time.Second),
		Fallback:            FallbackProxy,
		NoDataForKnownNames: &nodata,
		ChaseCNAME:          &chase,
		Log:                 LogSpec{Level: "info", Format: "console"},
	}
}

// Load reads a YAML config file and fills in defaults for anything omitted.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.Path = path
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// ApplyDefaults fills unset fields with their defaults.
func (c *Config) ApplyDefaults() {
	d := Default()
	if len(c.Listen) == 0 {
		c.Listen = d.Listen
	}
	if len(c.Upstream) == 0 {
		c.Upstream = d.Upstream
	}
	if c.TTL == 0 {
		c.TTL = d.TTL
	}
	if c.Timeout == 0 {
		c.Timeout = d.Timeout
	}
	if c.Fallback == "" {
		c.Fallback = d.Fallback
	}
	if c.NoDataForKnownNames == nil {
		c.NoDataForKnownNames = d.NoDataForKnownNames
	}
	if c.ChaseCNAME == nil {
		c.ChaseCNAME = d.ChaseCNAME
	}
	if c.Log.Level == "" {
		c.Log.Level = d.Log.Level
	}
	if c.Log.Format == "" {
		c.Log.Format = d.Log.Format
	}
}

// Validate checks the configuration without building anything.
func (c *Config) Validate() error {
	if !c.Fallback.Valid() {
		return fmt.Errorf("fallback: %q is not one of proxy, nxdomain, refused, empty", c.Fallback)
	}
	if len(c.Only) > 0 && len(c.Except) > 0 {
		return fmt.Errorf("only and except are mutually exclusive")
	}
	if _, err := c.BuildStore(); err != nil {
		return err
	}
	return nil
}

// BuildStore turns the record definitions into a lookup store.
func (c *Config) BuildStore() (*records.Store, error) {
	store := records.NewStore(c.TTL)

	names := make([]string, 0, len(c.Hosts))
	for name := range c.Hosts {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		for _, addr := range c.Hosts[name] {
			rtype, err := addressType(addr)
			if err != nil {
				return nil, fmt.Errorf("hosts: %s: %w", name, err)
			}
			r, err := records.New(name, rtype, addr, 0)
			if err != nil {
				return nil, fmt.Errorf("hosts: %w", err)
			}
			store.Add(r)
		}
	}

	for i, spec := range c.Records {
		values := append(append(StringList{}, spec.Value...), spec.Values...)
		if len(values) == 0 {
			return nil, fmt.Errorf("records[%d] (%s): no value", i, spec.Name)
		}
		for _, v := range values {
			r, err := records.New(spec.Name, spec.Type, v, spec.TTL)
			if err != nil {
				return nil, fmt.Errorf("records[%d]: %w", i, err)
			}
			store.Add(r)
		}
	}

	return store, nil
}

// BuildFilter turns the only/except lists into a runtime filter.
func (c *Config) BuildFilter() *records.Filter {
	switch {
	case len(c.Only) > 0:
		return records.NewFilter(records.FilterOnly, c.Only)
	case len(c.Except) > 0:
		return records.NewFilter(records.FilterExcept, c.Except)
	default:
		return records.NewFilter(records.FilterNone, nil)
	}
}

// Marshal renders the config as YAML.
func (c *Config) Marshal() ([]byte, error) {
	return yaml.Marshal(c)
}

// addressType picks A or AAAA for a bare address in the hosts shorthand.
func addressType(addr string) (string, error) {
	ip := net.ParseIP(strings.TrimSpace(addr))
	if ip == nil {
		return "", fmt.Errorf("%q is not an IP address; use the records: section for non-address types", addr)
	}
	if ip.To4() != nil {
		return "A", nil
	}
	return "AAAA", nil
}

// ParseRecordFlag parses a --record value from the command line.
//
// Accepted forms:
//
//	name=value            type inferred (A/AAAA for IPs, otherwise CNAME)
//	TYPE:name=value       explicit type
//	TYPE:name=value#ttl   explicit type and TTL
func ParseRecordFlag(s string) (RecordSpec, error) {
	spec := RecordSpec{}

	if at := strings.LastIndex(s, "#"); at != -1 {
		var ttl uint32
		if _, err := fmt.Sscanf(s[at+1:], "%d", &ttl); err != nil {
			return spec, fmt.Errorf("%q: bad TTL after '#'", s)
		}
		spec.TTL = ttl
		s = s[:at]
	}

	eq := strings.Index(s, "=")
	if eq == -1 {
		return spec, fmt.Errorf("%q: want name=value or TYPE:name=value", s)
	}
	left, value := strings.TrimSpace(s[:eq]), strings.TrimSpace(s[eq+1:])
	if value == "" {
		return spec, fmt.Errorf("%q: empty value", s)
	}

	if colon := strings.Index(left, ":"); colon != -1 {
		spec.Type = strings.TrimSpace(left[:colon])
		spec.Name = strings.TrimSpace(left[colon+1:])
		if _, ok := records.ParseType(spec.Type); !ok {
			return spec, fmt.Errorf("%q: unknown record type %q", s, spec.Type)
		}
	} else {
		spec.Name = left
		if t, err := addressType(value); err == nil {
			spec.Type = t
		} else {
			spec.Type = "CNAME"
		}
	}

	if spec.Name == "" {
		return spec, fmt.Errorf("%q: empty name", s)
	}
	spec.Value = StringList{value}
	return spec, nil
}

// SupportedTypes lists the record types dnswizard understands, for help text.
func SupportedTypes() []string {
	common := []uint16{
		dns.TypeA, dns.TypeAAAA, dns.TypeCNAME, dns.TypeMX, dns.TypeNS,
		dns.TypePTR, dns.TypeTXT, dns.TypeSOA, dns.TypeSRV, dns.TypeNAPTR,
		dns.TypeCAA, dns.TypeDNSKEY, dns.TypeRRSIG,
	}
	out := make([]string, 0, len(common))
	for _, t := range common {
		out = append(out, records.TypeName(t))
	}
	return out
}
