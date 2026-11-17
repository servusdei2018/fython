package vm

import "github.com/servusdei2018/fython/pkg/val"

// Pre-interned string constants used across pkg/vm to avoid repeated runtime
// InternString hashing and pool lookups on hot paths.
var (
	nameMetaclass        = val.InternString("metaclass")
	nameClassCell        = val.InternString("__classcell__")
	nameDunderName       = val.InternString("__name__")
	nameModule           = val.InternString("__module__")
	nameQualname         = val.InternString("__qualname__")
	nameSys              = val.InternString("sys")
	nameExcepthook       = val.InternString("excepthook")
	nameDunderExcepthook = val.InternString("__excepthook__")
	nameTraceback        = val.InternString("__traceback__")
	nameAnnotate         = val.InternString("__annotate__")
	nameCause            = val.InternString("__cause__")
	nameSuppressContext  = val.InternString("__suppress_context__")
	nameAwait            = val.InternString("__await__")
	nameMatchArgs        = val.InternString("__match_args__")
	nameDelitem          = val.InternString("__delitem__")
	nameNext             = val.InternString("__next__")
	nameGetitem          = val.InternString("__getitem__")
	nameExit             = val.InternString("__exit__")
	nameInstanceCheck    = val.InternString("__instancecheck__")
	nameSubclassCheck    = val.InternString("__subclasscheck__")
	nameClass            = val.InternString("__class__")
)
