package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestFycRoundTripSetAndNestedTuple(t *testing.T) {
	if vm.FycVersion < 9 {
		t.Fatalf("expected FycVersion >= 9, got %d", vm.FycVersion)
	}
	src := `
def check_membership(x):
    return x in ("alpha", "beta", (1, (2, 3)), b"raw", 42, None, True)

def get_nested():
    return ((1, "a"), (2, ("b", b"c")), ())

result = (
    check_membership("alpha"),
    check_membership((1, (2, 3))),
    check_membership(b"raw"),
    check_membership("missing"),
    get_nested(),
)
`
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	hash := vm.ComputeSourceHash([]byte(src))
	raw, err := vm.SerializeCodeObject(co, uint64(len(src)), 123456789, hash)
	if err != nil {
		t.Fatalf("SerializeCodeObject failed: %v", err)
	}
	co2, hdr, err := vm.DeserializeCodeObject(raw)
	if err != nil {
		t.Fatalf("DeserializeCodeObject failed: %v", err)
	}
	if !vm.ValidateFyc(hdr, uint64(len(src)), 123456789, hash) {
		t.Fatal("ValidateFyc failed for round-tripped header")
	}

	// Confirm the deserialized check_membership function still holds a KindSet constant.
	foundSet := false
	foundNestedTuple := false
	for _, c := range co2.Consts {
		if c.Kind() != val.KindCode {
			continue
		}
		sub := vm.AsCodeObject(c)
		for _, sc := range sub.Consts {
			if sc.Kind() == val.KindSet {
				foundSet = true
			}
			if sc.Kind() == val.KindTuple && len(sc.AsTuple().Items) == 3 && sc.AsTuple().Items[0].Kind() == val.KindTuple {
				foundNestedTuple = true
			}
		}
	}
	if !foundSet {
		t.Fatal("expected KindSet constant to survive .fyc round-trip")
	}
	if !foundNestedTuple {
		t.Fatal("expected nested KindTuple constant to survive .fyc round-trip")
	}

	eng := vm.NewEngine()
	v := eng.NewVM()
	defer eng.ReleaseVM(v)
	v.RunCode(co2)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unhandled exception: %v", exc)
	}
	res, ok := v.GetGlobal("result")
	if !ok || !res.IsTuple() {
		t.Fatalf("expected tuple result, got %v", res)
	}
	items := res.AsTuple().Items
	if !items[0].AsBool() || !items[1].AsBool() || !items[2].AsBool() || items[3].AsBool() {
		t.Fatalf("unexpected membership results after .fyc round-trip: %v", items[:4])
	}
	if !items[4].IsTuple() || len(items[4].AsTuple().Items) != 3 {
		t.Fatalf("unexpected nested tuple result: %v", items[4])
	}
}
