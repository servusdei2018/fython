package val

// Pre-interned string constants used across pkg/val to avoid repeated
// runtime InternString hashing and pool lookups on hot paths.
var (
	strArgs            = InternString("args")
	strTraceback       = InternString("__traceback__")
	strCause           = InternString("__cause__")
	strContext         = InternString("__context__")
	strSuppressContext = InternString("__suppress_context__")
	strClass           = InternString("__class__")
	strDict            = InternString("__dict__")
	strDoc             = InternString("__doc__")
	strBool            = InternString("__bool__")
	strHash            = InternString("__hash__")
	strDelete          = InternString("__delete__")
	strAnnotate        = InternString("__annotate__")
	strAnnotations     = InternString("__annotations__")
	strQualname        = InternString("__qualname__")
	strTypeParams      = InternString("__type_params__")
	strModule          = InternString("__module__")
	strBuiltins        = InternString("builtins")
	strBases           = InternString("__bases__")
	strName            = InternString("__name__")
	strMRO             = InternString("__mro__")
	strFile            = InternString("__file__")
)
