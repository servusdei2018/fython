package vm_test

import "testing"

func TestCPythonBisectSuite(t *testing.T) {
	runSnippet(t, `
import unittest
from test import support
import bisect

class TestBisect(unittest.TestCase):
    def test_search(self):
        data = [20, 30, 40, 50]
        self.assertEqual(bisect.bisect_left(data, 35), 2)
        self.assertEqual(bisect.bisect_right(data, 35), 2)
        self.assertEqual(bisect.bisect_left(data, 20), 0)
        self.assertEqual(bisect.bisect_right(data, 20), 1)

    def test_insort(self):
        data = [10, 20, 30]
        bisect.insort_left(data, 25)
        self.assertEqual(data, [10, 20, 25, 30])
        bisect.insort_right(data, 25)
        self.assertEqual(data, [10, 20, 25, 25, 30])

support.run_unittest(TestBisect)
`)
}

func TestCPythonHeapqSuite(t *testing.T) {
	runSnippet(t, `
import unittest
from test import support
import heapq

class TestHeapq(unittest.TestCase):
    def test_heapify_and_pop(self):
        h = [5, 3, 1, 4, 2]
        heapq.heapify(h)
        res = [heapq.heappop(h) for _ in range(5)]
        self.assertEqual(res, [1, 2, 3, 4, 5])

    def test_heappushpop(self):
        h = [10, 20, 30]
        self.assertEqual(heapq.heappushpop(h, 5), 5)
        self.assertEqual(heapq.heappushpop(h, 25), 10)

support.run_unittest(TestHeapq)
`)
}

func TestCPythonFnmatchSuite(t *testing.T) {
	runSnippet(t, `
import unittest
from test import support
import fnmatch

class TestFnmatch(unittest.TestCase):
    def test_fnmatch(self):
        self.assertTrue(fnmatch.fnmatch("foo.txt", "*.txt"))
        self.assertFalse(fnmatch.fnmatch("foo.txt", "*.py"))
        self.assertEqual(fnmatch.filter(["a.py", "b.txt", "c.py"], "*.py"), ["a.py", "c.py"])

support.run_unittest(TestFnmatch)
`)
}

func TestCPythonUUIDSuite(t *testing.T) {
	runSnippet(t, `
import unittest
from test import support
import uuid

class TestUUID(unittest.TestCase):
    def test_uuid4(self):
        u1 = uuid.uuid4()
        u2 = uuid.uuid4()
        self.assertNotEqual(u1, u2)
        self.assertEqual(len(str(u1)), 36)
        self.assertEqual(u1.version, 4)

support.run_unittest(TestUUID)
`)
}

func TestCPythonDictSuite(t *testing.T) {
	runSnippet(t, `
import unittest
from test import support

class TestDict(unittest.TestCase):
    def test_dict_ops(self):
        d = {"a": 1, "b": 2, "c": 3}
        self.assertEqual(d.get("a"), 1)
        self.assertEqual(d.get("missing", 99), 99)
        self.assertEqual(d.pop("b"), 2)
        self.assertEqual(list(d.keys()), ["a", "c"])
        self.assertEqual(list(d.values()), [1, 3])
        self.assertIn("a", d)
        self.assertNotIn("b", d)

support.run_unittest(TestDict)
`)
}

func TestCPythonListSuite(t *testing.T) {
	runSnippet(t, `
import unittest
from test import support

class TestList(unittest.TestCase):
    def test_list_ops(self):
        l = [1, 2, 3]
        l.append(4)
        self.assertEqual(l, [1, 2, 3, 4])
        l.extend([5, 6])
        self.assertEqual(l, [1, 2, 3, 4, 5, 6])
        self.assertEqual(l.pop(), 6)
        l.reverse()
        self.assertEqual(l, [5, 4, 3, 2, 1])

support.run_unittest(TestList)
`)
}

func TestCPythonSetSuite(t *testing.T) {
	runSnippet(t, `
import unittest
from test import support

class TestSet(unittest.TestCase):
    def test_set_ops(self):
        s = {1, 2, 3}
        s.add(4)
        self.assertIn(4, s)
        self.assertEqual(s - {2, 3}, {1, 4})
        self.assertEqual(s | {5}, {1, 2, 3, 4, 5})
        self.assertEqual(s & {3, 4, 5}, {3, 4})

support.run_unittest(TestSet)
`)
}

func TestCPythonTupleSuite(t *testing.T) {
	runSnippet(t, `
import unittest
from test import support

class TestTuple(unittest.TestCase):
    def test_tuple_ops(self):
        t = (1, 2, 3, 2)
        self.assertEqual(len(t), 4)
        self.assertEqual(t[0], 1)
        self.assertEqual(t.count(2), 2)
        self.assertEqual(t.index(3), 2)

support.run_unittest(TestTuple)
`)
}
