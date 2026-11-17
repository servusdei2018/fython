import time

from packaging.markers import Marker
from packaging.requirements import Requirement
from packaging.specifiers import SpecifierSet
from packaging.tags import sys_tags
from packaging.version import Version

REQUIREMENTS = [
    "package{idx} >= {lo}.0, < {hi}.0; python_version >= '3.9' and platform_machine == 'x86_64'".format(
        idx=i % 20, lo=i % 5, hi=(i % 5) + 2
    )
    for i in range(2000)
]

CANDIDATE_VERSIONS = [Version("1.{0}.0".format(i)) for i in range(50)]

ENV = {
    "python_version": "3.15",
    "python_full_version": "3.15.0",
    "os_name": "posix",
    "sys_platform": "linux",
    "platform_machine": "x86_64",
    "platform_python_implementation": "CPython",
    "implementation_name": "cpython",
    "implementation_version": "3.15.0",
}

TAG_LIST = list(sys_tags())


def resolve():
    total_matches = 0
    for req_str in REQUIREMENTS:
        req = Requirement(req_str)
        if req.marker is not None and not req.marker.evaluate(ENV):
            continue
        spec = req.specifier
        for version in CANDIDATE_VERSIONS:
            if version in spec:
                total_matches += 1

    # PEP 425 tag resolution against candidate wheel tags.
    wheel_tags = {str(tag) for tag in TAG_LIST[:12]}
    tag_matches = sum(1 for tag in TAG_LIST if str(tag) in wheel_tags)

    # Marker parsing stress.
    markers = [Marker("python_version >= '3.9'"), Marker("os_name == 'posix'")]
    marker_hits = sum(1 for marker in markers if marker.evaluate(ENV))

    return total_matches + tag_matches + marker_hits


def run_benchmark():
    # warmup
    resolve()
    t0 = time.perf_counter()
    result = resolve()
    elapsed = time.perf_counter() - t0
    return result, elapsed


if __name__ == "__main__":
    result, elapsed = run_benchmark()
    print(f"Packaging Resolution Benchmark completed in {elapsed * 1000:.2f} ms ({result} matches)")