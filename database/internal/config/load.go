/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/nil-go/konf"
	"github.com/nil-go/konf/provider/env"
	kfile "github.com/nil-go/konf/provider/file"
)

const envPrefix = "DBAAS_"

// Load registers and parses the operator flags, then resolves configuration in
// ascending precedence: file, environment, and explicitly supplied flags.
// Built-in defaults live in the typed target and are retained for absent keys.
func Load(set *flag.FlagSet, args []string) (Config, error) {
	return load(set, args, DefaultFilePath)
}

func load(set *flag.FlagSet, args []string, configFilePath string) (Config, error) {
	if set == nil {
		return Config{}, errors.New("configuration flag set must not be nil")
	}

	defaults := Default()
	bindFlags(set, defaults)
	if err := set.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parse configuration flags: %w", err)
	}

	store := konf.New(konf.WithMapKeyCaseSensitive())
	if err := loadFileIfPresent(store, configFilePath); err != nil {
		return Config{}, err
	}
	if err := store.Load(env.New(
		env.WithPrefix(envPrefix),
		env.WithNameSplitter(splitEnvironmentName),
	)); err != nil {
		return Config{}, fmt.Errorf("load environment configuration: %w", err)
	}
	if err := store.Load(explicitFlags{set: set}); err != nil {
		return Config{}, fmt.Errorf("load flag configuration: %w", err)
	}
	if store.Exists([]string{"operator", "namespace"}) {
		return Config{}, errors.New(
			"operator.namespace is installation metadata; set the Kustomize namespace instead",
		)
	}

	resolved := defaults
	if err := store.Unmarshal("", &resolved); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	if err := resolved.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate configuration: %w", err)
	}
	return resolved, nil
}

func loadFileIfPresent(store *konf.Config, path string) error {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		if err := store.Load(kfile.New(path)); err != nil {
			return fmt.Errorf("load configuration file %q: %w", path, err)
		}
		return nil
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("stat configuration file %q: %w", path, err)
	}
}

func splitEnvironmentName(name string) []string {
	if !strings.HasPrefix(name, envPrefix) {
		return nil
	}
	name = strings.TrimPrefix(name, envPrefix)
	raw := strings.Split(name, "__")
	keys := make([]string, 0, len(raw))
	for _, segment := range raw {
		if segment == "" {
			return nil
		}
		keys = append(keys, screamingSnakeToLowerCamel(segment))
	}
	return keys
}

func screamingSnakeToLowerCamel(value string) string {
	parts := strings.Split(strings.ToLower(value), "_")
	var out strings.Builder
	for index, part := range parts {
		if part == "" {
			continue
		}
		if index == 0 {
			out.WriteString(part)
			continue
		}
		runes := []rune(part)
		out.WriteRune(unicode.ToUpper(runes[0]))
		out.WriteString(string(runes[1:]))
	}
	return out.String()
}

// explicitFlags is deliberately visit-only. konf's stock flag provider also
// considers registered defaults, which makes an explicit value equal to its
// default unable to override a lower-precedence source.
type explicitFlags struct {
	set *flag.FlagSet
}

func (loader explicitFlags) Load() (map[string]any, error) {
	values := make(map[string]any)
	loader.set.Visit(func(item *flag.Flag) {
		getter, ok := item.Value.(flag.Getter)
		if !ok {
			insert(values, strings.Split(item.Name, "."), item.Value.String())
			return
		}
		insert(values, strings.Split(item.Name, "."), getter.Get())
	})
	return values, nil
}

func (explicitFlags) String() string { return "explicit flags" }

func insert(target map[string]any, keys []string, value any) {
	current := target
	for _, key := range keys[:len(keys)-1] {
		next, ok := current[key].(map[string]any)
		if !ok {
			next = make(map[string]any)
			current[key] = next
		}
		current = next
	}
	current[keys[len(keys)-1]] = value
}
