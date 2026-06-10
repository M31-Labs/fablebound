package policy

import (
	"reflect"
	"strings"
)

// ContextMap converts any struct with `arb` tags into a nested map[string]any
// mirroring the dot-path notation used by Arbiter policy expressions.
// This single map feeds both EvalGoverned (rule evaluation) and
// prog.Strategies.Evaluate (strategy routing) so replay sees identical context.
func ContextMap(v any) map[string]any {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return map[string]any{}
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return map[string]any{}
	}
	out := make(map[string]any)
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("arb")
		if tag == "" || tag == "-" {
			continue
		}
		segs := strings.Split(tag, ".")
		setNested(out, segs, rv.Field(i).Interface())
	}
	return out
}

// setNested writes value into m at the path described by segs,
// creating intermediate map[string]any nodes as needed.
func setNested(m map[string]any, segs []string, value any) {
	if len(segs) == 1 {
		m[segs[0]] = value
		return
	}
	cur := m
	for i, seg := range segs {
		if i == len(segs)-1 {
			cur[seg] = value
			return
		}
		next, ok := cur[seg]
		if !ok {
			child := make(map[string]any)
			cur[seg] = child
			cur = child
		} else {
			child, ok := next.(map[string]any)
			if !ok {
				child = make(map[string]any)
				cur[seg] = child
			}
			cur = child
		}
	}
}
