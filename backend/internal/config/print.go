package config

import (
	"encoding"
	"reflect"

	"go.yaml.in/yaml/v3"
)

// YAML renders the effective configuration using configuration key names.
// Fields tagged `redact:"true"` are replaced by "[REDACTED]".
func (c *Config) YAML() ([]byte, error) {
	return yaml.Marshal(toTree(reflect.ValueOf(*c)))
}

var textMarshaler = reflect.TypeFor[encoding.TextMarshaler]()

func toTree(v reflect.Value) any {
	if v.Type().Implements(textMarshaler) {
		b, err := v.Interface().(encoding.TextMarshaler).MarshalText()
		if err == nil {
			return string(b)
		}
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		return toTree(v.Elem())
	case reflect.Struct:
		out := make(map[string]any, v.NumField())
		for i := 0; i < v.NumField(); i++ {
			sf := v.Type().Field(i)
			tag := sf.Tag.Get("koanf")
			if tag == "" || tag == "-" {
				continue
			}
			if sf.Tag.Get("redact") == "true" && !v.Field(i).IsZero() {
				out[tag] = "[REDACTED]"
				continue
			}
			out[tag] = toTree(v.Field(i))
		}
		return out
	case reflect.Slice:
		out := make([]any, v.Len())
		for i := range out {
			out[i] = toTree(v.Index(i))
		}
		return out
	case reflect.Map:
		out := make(map[string]any, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out[iter.Key().String()] = toTree(iter.Value())
		}
		return out
	}
	return v.Interface()
}
