package vm

import (
	"math"
	"math/big"

	"github.com/servusdei2018/fython/pkg/val"
)

func (e *Engine) createTestCapiModule() val.Value {
	modVal := val.NewModuleVal("_testcapi", "CPython test C-API bridge")
	d := modVal.AsModule().Dict

	set := func(name string, v val.Value) {
		d.Set(val.InternString(name), v)
	}

	set("UINT_MAX", val.Int64Val(math.MaxUint32))
	set("INT_MAX", val.Int64Val(math.MaxInt32))
	set("INT_MIN", val.Int64Val(math.MinInt32))
	set("LLONG_MAX", val.Int64Val(math.MaxInt64))
	set("LLONG_MIN", val.Int64Val(math.MinInt64))
	set("ULONG_MAX", val.NewBigIntVal(new(big.Int).SetUint64(math.MaxUint64)))
	set("CHAR_BIT", val.Int64Val(8))
	set("SIZEOF_VOID_P", val.Int64Val(8))
	set("SIZEOF_TIME_T", val.Int64Val(8))

	set("with_tp_del", val.NewBuiltinFuncVal("with_tp_del", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			return args[0]
		}
		return val.NoneVal()
	}))

	set("get_recursion_depth", val.NewBuiltinFuncVal("get_recursion_depth", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(1)
	}))

	set("set_recursion_depth", val.NewBuiltinFuncVal("set_recursion_depth", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	return modVal
}

func (e *Engine) createTestPackage() (val.Value, val.Value) {
	testModVal := val.NewModuleVal("test", "CPython test package")
	testMod := testModVal.AsModule()

	supportModVal := val.NewModuleVal("test.support", "CPython test support module")
	supportMod := supportModVal.AsModule()

	testMod.Dict.Set(val.InternString("support"), supportModVal)

	set := func(name string, v val.Value) {
		supportMod.Dict.Set(val.InternString(name), v)
	}

	set("verbose", val.Int64Val(0))
	cpythonOnlyVal := val.NewBuiltinFuncVal("cpython_only", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			return args[0]
		}
		return val.NoneVal()
	})
	set("cpython_only", cpythonOnlyVal)

	set("requires", val.NewBuiltinFuncVal("requires", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewBuiltinFuncVal("requires_decorator", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) > 0 {
				return args[0]
			}
			return val.NoneVal()
		})
	}))

	set("is_resource_enabled", val.NewBuiltinFuncVal("is_resource_enabled", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(false)
	}))

	set("run_unittest", val.NewBuiltinFuncVal("run_unittest", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if ctx != nil && ctx.VM != nil {
			vmInstance := ctx.VM.(*VM)
			unittestMod, err := vmInstance.ImportModule("unittest", 0, true, nil)
			if err != nil {
				panic(err)
			}
			suiteCls, _ := vmInstance.ImportFrom(unittestMod, "TestSuite", nil)
			suiteVal := ctx.Invoke0(suiteCls)
			addTest, _, _ := suiteVal.Type().LookupMRO(val.InternString("addTest"))

			testLoader, _ := vmInstance.ImportFrom(unittestMod, "defaultTestLoader", nil)
			loadTests, _, _ := testLoader.Type().LookupMRO(val.InternString("loadTestsFromTestCase"))

			for _, cls := range args {
				tc := ctx.Invoke2(loadTests, testLoader, cls)
				ctx.Invoke2(addTest, suiteVal, tc)
			}

			ioMod, _ := vmInstance.ImportModule("io", 0, true, nil)
			stringIOCls, _ := vmInstance.ImportFrom(ioMod, "StringIO", nil)
			streamVal := ctx.Invoke0(stringIOCls)

			textRunnerCls, _ := vmInstance.ImportFrom(unittestMod, "TextTestRunner", nil)
			runnerVal := ctx.Invoke1(textRunnerCls, streamVal)
			runMethod, _, _ := runnerVal.Type().LookupMRO(val.InternString("run"))

			res := ctx.Invoke2(runMethod, runnerVal, suiteVal)
			wasSuccessful, _, _ := res.Type().LookupMRO(val.InternString("wasSuccessful"))
			if !ctx.Invoke1(wasSuccessful, res).IsTruthy() {
				panic("AssertionError: test suite failed")
			}
		}
		return val.NoneVal()
	}))

	// Sub-helpers
	importHelperVal := val.NewModuleVal("test.support.import_helper", "")
	ihDict := importHelperVal.AsModule().Dict
	ihDict.Set(val.InternString("import_module"), val.NewBuiltinFuncVal("import_module", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: import_module takes at least 1 argument")
		}
		modName := args[0].AsString()
		vmInstance := ctx.VM.(*VM)
		mod, err := vmInstance.ImportModule(modName, 0, true, nil)
		if err != nil {
			panic(err.Error())
		}
		return mod
	}))
	ihDict.Set(val.InternString("cpython_only"), cpythonOnlyVal)
	set("import_helper", importHelperVal)

	osHelperVal := val.NewModuleVal("test.support.os_helper", "")
	ohDict := osHelperVal.AsModule().Dict
	ohDict.Set(val.InternString("TESTFN"), val.NewStringVal("@test"))
	set("os_helper", osHelperVal)

	warningsHelperVal := val.NewModuleVal("test.support.warnings_helper", "")
	whDict := warningsHelperVal.AsModule().Dict
	whDict.Set(val.InternString("ignore_warnings"), val.NewBuiltinFuncVal("ignore_warnings", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewBuiltinFuncVal("ignore_warnings_decorator", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) > 0 {
				return args[0]
			}
			return val.NoneVal()
		})
	}))
	set("warnings_helper", warningsHelperVal)

	return testModVal, supportModVal
}
