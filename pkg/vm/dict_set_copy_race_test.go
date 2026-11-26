package vm_test

import (
	"testing"
)

func TestDictSetCopyConcurrentRace(t *testing.T) {
	runSnippet(t, `
import threading

shared_dict = {f"k{i}": i for i in range(16)}
shared_set = set(shared_dict)
errors = []

def worker(tid):
    try:
        for _ in range(30):
            c = shared_dict.copy()
            c.update({"k0": 99 + tid, "new": tid})
            assert c["k0"] == 99 + tid and shared_dict["k0"] == 0
            s2 = shared_set.copy()
            s2.add(f"extra_{tid}")
            assert f"extra_{tid}" not in shared_set
    except Exception as e:
        errors.append(e)

threads = [threading.Thread(target=worker, args=(i,)) for i in range(4)]
for th in threads:
    th.start()
for th in threads:
    th.join()
assert not errors, f"concurrent errors: {errors}"
`)
}
