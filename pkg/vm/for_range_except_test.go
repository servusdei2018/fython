package vm_test

import "testing"

func TestFunctionRangeLoopCaughtException(t *testing.T) {
	runSnippet(t, `
def f():
    out = []
    for i in range(5):
        try:
            if i == 1:
                raise ValueError("boom")
            out.append(i)
        except ValueError:
            out.append(100+i)
    return out
assert f() == [0, 101, 2, 3, 4], f()

def g():
    out = []
    for i in range(5):
        try:
            if i % 2 == 0:
                raise RuntimeError("e")
            out.append(i)
        except RuntimeError:
            continue
    return out
assert g() == [1, 3], g()

def h():
    out = []
    for i in range(5):
        try:
            if i == 3:
                break
            if i == 1:
                raise ValueError("x")
            out.append(i)
        except ValueError:
            out.append(99)
            continue
    return out
assert h() == [0, 99, 2], h()

def nested():
    out = []
    for i in range(5):
        try:
            try:
                if i == 2:
                    raise KeyError("inner")
                out.append(i)
            except KeyError:
                raise ValueError("outer")
        except ValueError:
            out.append(200+i)
    return out
assert nested() == [0, 1, 202, 3, 4], nested()

def tworange():
    out = []
    for i in range(2, 6):
        try:
            if i == 4:
                raise ValueError("boom")
            out.append(i)
        except ValueError:
            out.append(100+i)
    return out
assert tworange() == [2, 3, 104, 5], tworange()
`)
}
