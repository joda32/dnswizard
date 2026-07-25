// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

package records

import (
	"sort"
	"strings"
)

// Store holds every configured fake record, indexed by type.
//
// A Store is immutable once built; hot-reloading swaps in a whole new one, so
// in-flight queries never see a half-applied config.
type Store struct {
	byType     map[uint16][]Record
	all        []Record
	defaultTTL uint32
}

// NewStore returns an empty store. A zero defaultTTL falls back to DefaultTTL.
func NewStore(defaultTTL uint32) *Store {
	if defaultTTL == 0 {
		defaultTTL = DefaultTTL
	}
	return &Store{byType: make(map[uint16][]Record), defaultTTL: defaultTTL}
}

// DefaultTTL reports the TTL applied to records that do not set their own.
func (s *Store) DefaultTTL() uint32 { return s.defaultTTL }

// Add appends a record to the store.
func (s *Store) Add(r Record) {
	s.byType[r.Type] = append(s.byType[r.Type], r)
	s.all = append(s.all, r)
}

// Len reports how many records are configured.
func (s *Store) Len() int { return len(s.all) }

// All returns every record, sorted by name then type, for display purposes.
func (s *Store) All() []Record {
	out := append([]Record(nil), s.all...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Type != out[j].Type {
			return TypeName(out[i].Type) < TypeName(out[j].Type)
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// Lookup returns the records that answer qname for qtype.
//
// When several patterns match, only the most specific one wins — an exact name
// beats "*.example.com", which in turn beats "*". All records sharing that
// winning pattern are returned, so a name with several addresses answers with
// all of them.
func (s *Store) Lookup(qname string, qtype uint16) []Record {
	recs, _ := s.LookupScored(qname, qtype)
	return recs
}

// LookupScored is Lookup plus the specificity of the winning pattern, which
// lets callers compare matches across record types. The score is meaningful
// only in comparison with another score from this same function; -1 means no
// match at all.
func (s *Store) LookupScored(qname string, qtype uint16) ([]Record, int) {
	candidates := s.byType[qtype]
	if len(candidates) == 0 {
		return nil, -1
	}

	labels := reverseLabels(NormaliseName(qname))
	if len(labels) == 0 {
		return nil, -1
	}

	bestScore := -1
	var bestPattern string
	var matches []Record

	for _, r := range candidates {
		score, ok := matchPattern(r.Name, labels)
		if !ok {
			continue
		}
		switch {
		case score > bestScore:
			bestScore, bestPattern = score, r.Name
			matches = []Record{r}
		case score == bestScore && r.Name == bestPattern:
			matches = append(matches, r)
		}
	}
	return matches, bestScore
}

// TypesFor lists the record types that have a match for qname, in a stable
// order. Used to answer ANY queries with everything known about a name.
func (s *Store) TypesFor(qname string) []uint16 {
	labels := reverseLabels(NormaliseName(qname))
	if len(labels) == 0 {
		return nil
	}

	seen := make(map[uint16]bool)
	for _, r := range s.all {
		if seen[r.Type] {
			continue
		}
		if _, ok := matchPattern(r.Name, labels); ok {
			seen[r.Type] = true
		}
	}

	out := make([]uint16, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return TypeName(out[i]) < TypeName(out[j]) })
	return out
}

// Knows reports whether any record at all matches qname, regardless of type.
// The server uses this to answer NODATA instead of leaking a lab name to an
// upstream resolver.
func (s *Store) Knows(qname string) bool {
	labels := reverseLabels(NormaliseName(qname))
	if len(labels) == 0 {
		return false
	}
	for _, r := range s.all {
		if _, ok := matchPattern(r.Name, labels); ok {
			return true
		}
	}
	return false
}

// matchPattern reports whether a pattern matches the already-reversed labels of
// a query name, and how specific the match is (higher is more specific).
//
// Matching rules:
//   - a literal label must match exactly (case was folded by the caller);
//   - "*" as the leftmost label matches one or more labels, so
//     "*.example.com" covers both "a.example.com" and "a.b.example.com";
//   - "*" anywhere else matches exactly one label, e.g. "_sip._tcp.*.lab";
//   - "*" on its own matches every name.
//
// Note this is stricter than comparing only as many labels as the shorter of
// the two lists, which would let "example.com" silently match every subdomain
// and "*.example.com" match the bare apex. Here those are distinct patterns,
// so a pattern never matches more names than it literally spells out.
func matchPattern(pattern string, nameLabels []string) (int, bool) {
	patternLabels := reverseLabels(pattern)
	if len(patternLabels) == 0 {
		return 0, false
	}

	literals := 0
	for i, p := range patternLabels {
		last := i == len(patternLabels)-1

		if p == "*" {
			if last {
				// Leftmost wildcard: soak up every remaining label, but there
				// must be at least one for it to match.
				if len(nameLabels) <= i {
					return 0, false
				}
				return score(literals, len(patternLabels), true), true
			}
			if i >= len(nameLabels) {
				return 0, false
			}
			continue
		}

		if i >= len(nameLabels) || nameLabels[i] != p {
			return 0, false
		}
		literals++
	}

	if len(nameLabels) != len(patternLabels) {
		return 0, false
	}
	return score(literals, len(patternLabels), false), true
}

func score(literals, length int, leadingStar bool) int {
	s := literals*1000 + length*10
	if !leadingStar {
		// A fixed-length pattern is more specific than one that swallows an
		// arbitrary number of labels.
		s += 100000
	}
	return s
}

func reverseLabels(name string) []string {
	name = NormaliseName(name)
	if name == "" {
		return nil
	}
	parts := strings.Split(name, ".")
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return parts
}

// FilterMode selects how the domain filter treats a query.
type FilterMode int

const (
	// FilterNone fakes any name that has a matching record.
	FilterNone FilterMode = iota
	// FilterOnly fakes only names matching the listed patterns; everything
	// else is resolved for real.
	FilterOnly
	// FilterExcept resolves the listed patterns for real and fakes everything
	// else.
	FilterExcept
)

// Filter decides, before any record lookup, whether a name is even eligible to
// be faked.
type Filter struct {
	Mode     FilterMode
	Patterns []string
}

// NewFilter builds a filter from a mode and a list of name patterns.
func NewFilter(mode FilterMode, patterns []string) *Filter {
	f := &Filter{Mode: mode}
	for _, p := range patterns {
		if p = NormaliseName(p); p != "" {
			f.Patterns = append(f.Patterns, p)
		}
	}
	if len(f.Patterns) == 0 {
		f.Mode = FilterNone
	}
	return f
}

// ShouldFake reports whether qname is eligible for a faked answer.
func (f *Filter) ShouldFake(qname string) bool {
	if f == nil || f.Mode == FilterNone {
		return true
	}

	labels := reverseLabels(NormaliseName(qname))
	listed := false
	for _, p := range f.Patterns {
		if _, ok := matchPattern(p, labels); ok {
			listed = true
			break
		}
	}

	if f.Mode == FilterOnly {
		return listed
	}
	return !listed
}

// Describe renders the filter for logging.
func (f *Filter) Describe() string {
	switch {
	case f == nil || f.Mode == FilterNone:
		return "none"
	case f.Mode == FilterOnly:
		return "only " + strings.Join(f.Patterns, ",")
	default:
		return "all except " + strings.Join(f.Patterns, ",")
	}
}
