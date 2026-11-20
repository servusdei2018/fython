package vm

import "github.com/servusdei2018/fython/pkg/val"

// pyInt extracts a Python integer argument.
func pyInt(args []val.Value, i int, name string) int {
	if len(args) <= i {
		panic("TypeError: " + name + "() takes more arguments")
	}
	if !args[i].IsInt64() {
		panic("TypeError: " + name + "() argument must be an integer")
	}
	return int(args[i].AsInt64())
}

// pySeqItems returns the elements of a list or a tuple.
func pySeqItems(v val.Value) []val.Value {
	if v.IsList() {
		return v.AsList().Items
	}
	if v.IsTuple() {
		return v.AsTuple().Items
	}
	return nil
}
