// Package template provides text/template-compatible utility functions.
package template

import "reflect"

// NonZero reports whether val is non-zero in the sense of Go's
// text/template {{if}} actions: the empty values are false, 0, any
// nil pointer or interface value, and any array, slice, map, or
// string of length zero.
func NonZero(val any) bool {
	if val == nil {
		return false
	}
	v := reflect.ValueOf(val)
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() != 0
	case reflect.Float32, reflect.Float64:
		return v.Float() != 0
	case reflect.Complex64, reflect.Complex128:
		return v.Complex() != 0
	case reflect.String:
		return v.Len() > 0
	case reflect.Array, reflect.Slice, reflect.Map:
		return v.Len() > 0
	case reflect.Ptr, reflect.Interface:
		return !v.IsNil()
	case reflect.Struct:
		return !v.IsZero()
	default:
		return true
	}
}
