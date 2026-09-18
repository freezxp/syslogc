package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"
)

// EnvPrefix is the prefix of all configuration environment variables.
const EnvPrefix = "SYSLOGC_"

// LoadOptions controls where configuration is read from.
type LoadOptions struct {
	// File is an optional YAML file path.
	File string
	// Environ is the environment in "KEY=value" form (os.Environ() if nil).
	Environ []string
	// Flags is an optional parsed flag set created with RegisterFlags.
	Flags *pflag.FlagSet
}

// RegisterFlags adds one flag per scalar configuration key (e.g.
// --storage.victorialogs.insert_url) to fs. Lists of structs (sources)
// can only be configured in YAML.
func RegisterFlags(fs *pflag.FlagSet) {
	for _, k := range leafKeys() {
		if k.isSlice {
			fs.StringSlice(k.path, nil, "config: "+k.path+" (comma-separated)")
		} else {
			fs.String(k.path, "", "config: "+k.path)
		}
		_ = fs.MarkHidden(k.path)
	}
}

// Load builds the effective configuration: defaults < YAML < env < flags.
// It applies per-source defaults and validates the result.
func Load(opts LoadOptions) (*Config, error) {
	k := koanf.New(".")

	if opts.File != "" {
		if err := k.Load(file.Provider(opts.File), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("load config file %s: %w", opts.File, err)
		}
	}

	keys := leafKeys()
	envToKey := make(map[string]leafKey, len(keys))
	for _, lk := range keys {
		envToKey[EnvPrefix+strings.ToUpper(strings.ReplaceAll(lk.path, ".", "_"))] = lk
	}
	environ := opts.Environ
	if environ == nil {
		environ = os.Environ()
	}
	var unknownEnv []string
	envProvider := env.Provider(".", env.Opt{
		Prefix:      EnvPrefix,
		EnvironFunc: func() []string { return environ },
		TransformFunc: func(name, value string) (string, any) {
			lk, ok := envToKey[name]
			if !ok {
				unknownEnv = append(unknownEnv, name)
				return "", nil
			}
			if lk.isSlice {
				return lk.path, splitList(value)
			}
			return lk.path, value
		},
	})
	if err := k.Load(envProvider, nil); err != nil {
		return nil, fmt.Errorf("load environment: %w", err)
	}

	if opts.Flags != nil {
		known := make(map[string]bool, len(keys))
		for _, lk := range keys {
			known[lk.path] = true
		}
		fp := posflag.ProviderWithFlag(opts.Flags, ".", nil, func(f *pflag.Flag) (string, any) {
			// Skip unset flags and flags that are not configuration keys (e.g. --config).
			if !f.Changed || !known[f.Name] {
				return "", nil
			}
			return f.Name, posflag.FlagVal(opts.Flags, f)
		})
		if err := k.Load(fp, nil); err != nil {
			return nil, fmt.Errorf("load flags: %w", err)
		}
	}

	cfg := Default()
	err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{
		Tag: "koanf",
		DecoderConfig: &mapstructure.DecoderConfig{
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.TextUnmarshallerHookFunc(),
				mapstructure.StringToSliceHookFunc(","),
			),
			Result:           &cfg,
			WeaklyTypedInput: true,
			ErrorUnused:      true,
			TagName:          "koanf",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	for i := range cfg.Ingestion.Sources {
		applySourceDefaults(&cfg.Ingestion.Sources[i])
	}
	for i := range cfg.Forwarding.Targets {
		applyForwardDefaults(&cfg.Forwarding.Targets[i])
	}
	if cfg.Node.ID == "" {
		cfg.Node.ID, _ = os.Hostname()
	}

	var errs []error
	if len(unknownEnv) > 0 {
		sort.Strings(unknownEnv)
		errs = append(errs, fmt.Errorf("unknown environment variables: %s", strings.Join(unknownEnv, ", ")))
	}
	if err := cfg.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func splitList(v string) []string {
	parts := strings.Split(v, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

type leafKey struct {
	path    string
	isSlice bool
}

// leafKeys lists every scalar (or scalar-slice) key of Config, derived from
// koanf struct tags. Slices of structs and maps are excluded.
func leafKeys() []leafKey {
	var out []leafKey
	var walk func(t reflect.Type, prefix string)
	walk = func(t reflect.Type, prefix string) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("koanf")
			if tag == "" || tag == "-" {
				continue
			}
			path := tag
			if prefix != "" {
				path = prefix + "." + tag
			}
			ft := f.Type
			switch {
			case ft.Kind() == reflect.Struct:
				walk(ft, path)
			case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.String:
				out = append(out, leafKey{path: path, isSlice: true})
			case ft.Kind() == reflect.Slice || ft.Kind() == reflect.Map || ft.Kind() == reflect.Pointer:
				// not expressible as a single env var / flag
			default:
				out = append(out, leafKey{path: path})
			}
		}
	}
	walk(reflect.TypeFor[Config](), "")
	return out
}
