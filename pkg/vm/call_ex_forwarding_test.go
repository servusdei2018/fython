package vm_test

import (
	"testing"
)

func TestCallExPosOnlyAndForwarding(t *testing.T) {
	eng, v := runPySource(t, `
def fixed(a, b, c):
    return a * 100 + b * 10 + c

def varargs_only(*args):
    return (len(args), args)

def prefix_varargs(first, *rest):
    return (first, rest)

def forward_both(*args, **kwargs):
    # Mutating kwargs must NOT alias caller's dict.
    kwargs["mutated"] = 999
    return (args, kwargs)

def gen_callee(*args):
    for x in args:
        yield x * 2

t3 = (1, 2, 3)
assert fixed(*t3) == 123
assert varargs_only(*t3) == (3, (1, 2, 3))
assert prefix_varargs(*t3) == (1, (2, 3))
assert prefix_varargs(*(7,)) == (7, ())

caller_kw = {"k": 10}
a_out, kw_out = forward_both(*t3, **caller_kw)
assert a_out == (1, 2, 3)
assert kw_out == {"k": 10, "mutated": 999}
assert caller_kw == {"k": 10}, f"caller dict was mutated: {caller_kw}"

# Ephemeral dict literal forwarded into *args, **kwargs
a_out2, kw_out2 = forward_both(*(4, 5), **{"x": 1, "y": 2})
assert a_out2 == (4, 5)
assert kw_out2 == {"x": 1, "y": 2, "mutated": 999}

# Generator callee via CALL_EX must still return a generator
g = gen_callee(*t3)
assert list(g) == [2, 4, 6]
`)
	defer eng.ReleaseVM(v)
}
