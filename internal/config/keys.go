package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// field is one settable config key.
type field struct {
	get func(*Config) string
	set func(*Config, []string) error
}

func str(p func(*Config) *string) field {
	return field{
		get: func(c *Config) string { return *p(c) },
		set: func(c *Config, v []string) error {
			if len(v) != 1 {
				return fmt.Errorf("expected one value, got %d", len(v))
			}
			*p(c) = v[0]
			return nil
		},
	}
}

func list(p func(*Config) *[]string) field {
	return field{
		get: func(c *Config) string { return strings.Join(*p(c), "\n") },
		set: func(c *Config, v []string) error { *p(c) = slices.Clone(v); return nil },
	}
}

func boolean(p func(*Config) *bool) field {
	return field{
		get: func(c *Config) string { return strconv.FormatBool(*p(c)) },
		set: func(c *Config, v []string) error {
			if len(v) != 1 {
				return fmt.Errorf("expected true or false")
			}
			b, err := strconv.ParseBool(v[0])
			if err != nil {
				return fmt.Errorf("expected true or false, got %q", v[0])
			}
			*p(c) = b
			return nil
		},
	}
}

var fields = map[string]field{
	"paths":                        list(func(c *Config) *[]string { return &c.Paths }),
	"exclude":                      list(func(c *Config) *[]string { return &c.Exclude }),
	"schedule.enabled":             boolean(func(c *Config) *bool { return &c.Schedule.Enabled }),
	"schedule.every":               str(func(c *Config) *string { return &c.Schedule.Every }),
	"storage.backend":              str(func(c *Config) *string { return &c.Storage.Backend }),
	"storage.s3.endpoint":          str(func(c *Config) *string { return &c.Storage.S3.Endpoint }),
	"storage.s3.region":            str(func(c *Config) *string { return &c.Storage.S3.Region }),
	"storage.s3.bucket":            str(func(c *Config) *string { return &c.Storage.S3.Bucket }),
	"storage.s3.prefix":            str(func(c *Config) *string { return &c.Storage.S3.Prefix }),
	"storage.s3.access_key_id":     str(func(c *Config) *string { return &c.Storage.S3.AccessKeyID }),
	"storage.s3.secret_access_key": str(func(c *Config) *string { return &c.Storage.S3.SecretAccessKey }),
	"storage.s3.insecure":          boolean(func(c *Config) *bool { return &c.Storage.S3.Insecure }),
	"storage.permafrost.url":       str(func(c *Config) *string { return &c.Storage.Permafrost.URL }),
	"storage.permafrost.token":     str(func(c *Config) *string { return &c.Storage.Permafrost.Token }),
	"verify.sample": {
		get: func(c *Config) string { return strconv.Itoa(c.Verify.Sample) },
		set: func(c *Config, v []string) error {
			if len(v) != 1 {
				return fmt.Errorf("expected a number")
			}
			n, err := strconv.Atoi(v[0])
			if err != nil || n < 0 {
				return fmt.Errorf("expected a number of chunks, got %q", v[0])
			}
			c.Verify.Sample = n
			return nil
		},
	},
}

// Secret keys are masked by `frost config get` unless asked.
var secretKeys = []string{"storage.s3.secret_access_key", "storage.permafrost.token"}

// IsSecret reports whether key holds a credential.
func IsSecret(key string) bool { return slices.Contains(secretKeys, key) }

// Keys lists every settable key, sorted.
func Keys() []string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Get returns the value of a dotted key. Lists come back one item per line.
func (c *Config) Get(key string) (string, error) {
	f, ok := fields[key]
	if !ok {
		return "", unknownKey(key)
	}
	return f.get(c), nil
}

// Set updates a dotted key. Lists take one value per item.
func (c *Config) Set(key string, values []string) error {
	f, ok := fields[key]
	if !ok {
		return unknownKey(key)
	}
	if err := f.set(c, values); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

func unknownKey(key string) error {
	return fmt.Errorf("unknown config key %q, valid keys:\n  %s", key, strings.Join(Keys(), "\n  "))
}
