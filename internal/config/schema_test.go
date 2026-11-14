package config

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The JSON schemas are hand-written for editor support. This test fails
// when a config field is added or renamed without updating them.

type node map[string]any

func load(t *testing.T, path string) node {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var n node
	if err := json.Unmarshal(b, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (n node) resolve(root node) node {
	if ref, ok := n["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		return node(root["$defs"].(map[string]any)[name].(map[string]any))
	}
	return n
}

func (n node) props() node {
	p, _ := n["properties"].(map[string]any)
	return node(p)
}

func yamlKeys(t reflect.Type) map[string]reflect.Type {
	keys := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		if tag == "-" || tag == "" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if strings.Contains(opts, "inline") {
			for k, v := range yamlKeys(f.Type) {
				keys[k] = v
			}
			continue
		}
		keys[name] = f.Type
	}
	return keys
}

var leaf = reflect.TypeOf(Duration{})

func compare(t *testing.T, where string, typ reflect.Type, n, root node) {
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || typ == leaf {
		return
	}
	n = n.resolve(root)
	if items, ok := n["items"].(map[string]any); ok && n["properties"] == nil {
		n = node(items).resolve(root)
	}
	want := yamlKeys(typ)
	got := n.props()
	var missing, extra []string
	for k := range want {
		if _, ok := got[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("%s: schema missing %v, schema has unknown %v", where, missing, extra)
	}
	for k, ft := range want {
		if sub, ok := got[k].(map[string]any); ok {
			compare(t, where+"."+k, ft, node(sub), root)
		}
	}
}

func TestSchemasMatchConfigTypes(t *testing.T) {
	kg := load(t, "../../schema/keepgoing.schema.json")
	compare(t, "keepgoing", reflect.TypeOf(Config{}), kg, kg)
	fb := load(t, "../../schema/fallbacks.schema.json")
	compare(t, "fallbacks", reflect.TypeOf(Fallbacks{}), fb, fb)
	rt := load(t, "../../schema/routes.schema.json")
	compare(t, "routes", reflect.TypeOf(Routes{}), rt, rt)
}
