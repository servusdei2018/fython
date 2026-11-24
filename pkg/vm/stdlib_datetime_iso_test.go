package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/vm"
)

func TestDateTimeFromIsoFormatParity(t *testing.T) {
	code := `
import datetime

# Basic date-only
d = datetime.datetime.fromisoformat("2025-01-15")
assert (d.year, d.month, d.day, d.hour, d.minute, d.second, d.microsecond) == (2025, 1, 15, 0, 0, 0, 0)
assert d.tzinfo is None

# Standard datetime with T and space separators
dt1 = datetime.datetime.fromisoformat("2025-01-01T00:00:00")
assert (dt1.year, dt1.month, dt1.day, dt1.hour, dt1.minute, dt1.second, dt1.microsecond) == (2025, 1, 1, 0, 0, 0, 0)
assert dt1.tzinfo is None

dt2 = datetime.datetime.fromisoformat("2025-06-30 14:25:36")
assert (dt2.year, dt2.month, dt2.day, dt2.hour, dt2.minute, dt2.second) == (2025, 6, 30, 14, 25, 36)

# HH:MM without seconds
dt_hm = datetime.datetime.fromisoformat("2025-06-30T09:45")
assert (dt_hm.hour, dt_hm.minute, dt_hm.second) == (9, 45, 0)

# Fractional seconds (1, 3, 6 digits)
dt_f1 = datetime.datetime.fromisoformat("2025-01-01T12:34:56.5")
assert dt_f1.microsecond == 500000
dt_f3 = datetime.datetime.fromisoformat("2025-01-01T12:34:56.123")
assert dt_f3.microsecond == 123000
dt_f6 = datetime.datetime.fromisoformat("2025-01-01T12:34:56.000789")
assert dt_f6.microsecond == 789

# Timezone Z and +/-HH:MM
dt_z = datetime.datetime.fromisoformat("2025-01-01T12:00:00Z")
assert dt_z.tzinfo is not None and dt_z.utcoffset().total_seconds() == 0

dt_plus = datetime.datetime.fromisoformat("2025-01-01T12:00:00+05:30")
assert dt_plus.tzinfo is not None and dt_plus.utcoffset().total_seconds() == 5 * 3600 + 30 * 60

dt_minus = datetime.datetime.fromisoformat("2025-01-01T12:00:00.25-04:00")
assert dt_minus.microsecond == 250000
assert dt_minus.tzinfo is not None and dt_minus.utcoffset().total_seconds() == -4 * 3600

# Leap year valid
dt_leap = datetime.datetime.fromisoformat("2024-02-29T23:59:59")
assert (dt_leap.year, dt_leap.month, dt_leap.day) == (2024, 2, 29)

# Invalid inputs must raise ValueError
bad_inputs = [
    "",
    "not-a-date",
    "2025-02-29T00:00:00",
    "2025-13-01T00:00:00",
    "2025-01-32T00:00:00",
    "2025-01-01T25:00:00",
    "2025-01-01T12:60:00",
    "2025-01-01T12:00:60",
    "2025-01-01T12:00:00.",
    "2025-01-01T12:00:00Zextra",
    "2025-01-01T12:00:00+25:00",
]
for s in bad_inputs:
    raised = False
    try:
        datetime.datetime.fromisoformat(s)
    except ValueError:
        raised = True
    assert raised, f"expected ValueError for {s!r}"
`
	runPython(t, code)
	_ = vm.OP_NOP
}
