package vm_test

import (
	"testing"
)

func TestNegativeAttrGetattrDefaultAndRaise(t *testing.T) {
	runSnippet(t, `
class Cfg:
    pass
c = Cfg()
assert getattr(c, "missing_xyz", "dflt") == "dflt"
assert getattr(c, "missing_xyz", "dflt") == "dflt"
assert hasattr(c, "missing_xyz") is False
c.missing_xyz = 11
assert getattr(c, "missing_xyz", "dflt") == 11
assert hasattr(c, "missing_xyz") is True
try:
    getattr(c, "other_missing_xyz")
except AttributeError:
    pass
else:
    raise AssertionError("want AttributeError")
`)
}

func TestNegativeAttrClassAddInvalidates(t *testing.T) {
	runSnippet(t, `
class Box:
    pass
b = Box()
assert getattr(b, "k_xyz", 1) == 1
assert hasattr(b, "k_xyz") is False
Box.k_xyz = 5
assert getattr(b, "k_xyz", 1) == 5
assert hasattr(b, "k_xyz") is True
`)
}
