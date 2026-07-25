// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/joda32/dnswizard/internal/records"
)

// ImportINI reads a legacy INI record file and returns the equivalent
// dnswizard configuration.
//
// The INI format uses a section per record type and one "domain=value" pair per
// line:
//
//	[A]
//	*.example.com=192.0.2.1
//
// Wildcards carry over as written. They are matched more strictly here (see
// records.matchPattern): a bare "example.com" covered subdomains in the old
// format, while here it matches only the exact name. Import stays quiet about
// that — the README covers it.
func ImportINI(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := &Config{Hosts: map[string]StringList{}}

	var (
		section string
		lineNo  int
		skipped []string
	)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "[") {
			end := strings.Index(line, "]")
			if end == -1 {
				return nil, fmt.Errorf("%s:%d: unterminated section header", path, lineNo)
			}
			section = strings.ToUpper(strings.TrimSpace(line[1:end]))
			if _, ok := records.ParseType(section); !ok {
				skipped = append(skipped, section)
				section = ""
			}
			continue
		}

		if section == "" {
			continue // inside a section type we do not support
		}

		// ConfigParser accepts both "=" and ":" as separators, but ":" appears
		// inside IPv6 values, so only split on "=" here.
		eq := strings.Index(line, "=")
		if eq == -1 {
			return nil, fmt.Errorf("%s:%d: expected domain=value, got %q", path, lineNo, line)
		}
		name := strings.TrimSpace(line[:eq])
		value := strings.TrimSpace(line[eq+1:])
		if name == "" || value == "" {
			return nil, fmt.Errorf("%s:%d: empty name or value", path, lineNo)
		}

		// Validate now so a bad import fails loudly rather than at serve time.
		if _, err := records.New(name, section, value, 0); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}

		if section == "A" || section == "AAAA" {
			key := records.NormaliseName(name)
			cfg.Hosts[key] = append(cfg.Hosts[key], value)
			continue
		}
		cfg.Records = append(cfg.Records, RecordSpec{
			Name:  records.NormaliseName(name),
			Type:  section,
			Value: StringList{value},
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if len(cfg.Hosts) == 0 {
		cfg.Hosts = nil
	}
	if len(skipped) > 0 {
		return cfg, &SkippedSectionsError{Sections: skipped}
	}
	return cfg, nil
}

// SkippedSectionsError reports INI sections that were not recognised as record
// types. Import still succeeds; the caller decides how loud to be about it.
type SkippedSectionsError struct {
	Sections []string
}

func (e *SkippedSectionsError) Error() string {
	return "ignored unsupported sections: " + strings.Join(e.Sections, ", ")
}
