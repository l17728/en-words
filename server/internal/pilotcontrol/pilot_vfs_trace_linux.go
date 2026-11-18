//go:build cgo && pilot_e2e && linux

package pilotcontrol

/*
#cgo LDFLAGS: -pthread
#include <stdlib.h>
#include "../../third_party/go-sqlite3/sqlite3-binding.h"
int pilot_trace_register(const char *name, const char *expected_parent);
int pilot_trace_unregister(void);
const char *pilot_trace_parent_name(void);
int pilot_trace_capabilities_preserved(void);
void pilot_trace_reset(void);
int pilot_trace_overflowed(void);
void pilot_trace_test_set_overflowed(int overflow);
void pilot_trace_set_actor(const char *actor);
void pilot_trace_set_context(const char *run_id, const char *scenario_id, const char *case_id, const char *source_id);
int pilot_trace_copy(char *out, int cap);
void pilot_trace_set_pager_barrier(int control_fd, int trace_fd, const char *scenario_id);
int pilot_trace_flush_durable(void);
*/
import "C"

import (
	"fmt"
	"unsafe"
)

const pilotTraceVFSName = "pilot_vfs_trace_20261021"

func pilotTraceRegister(expectedParent string) error {
	name := C.CString(pilotTraceVFSName)
	defer C.free(unsafe.Pointer(name))
	parent := C.CString(expectedParent)
	defer C.free(unsafe.Pointer(parent))
	if rc := int(C.pilot_trace_register(name, parent)); rc != int(C.SQLITE_OK) {
		return fmt.Errorf("sqlite3_vfs_register(trace) rc=%d", rc)
	}
	return nil
}

func pilotTraceParentName() string { return C.GoString(C.pilot_trace_parent_name()) }

func pilotTraceCapabilitiesPreserved() bool { return C.pilot_trace_capabilities_preserved() != 0 }

func pilotTraceUnregister() error {
	if rc := int(C.pilot_trace_unregister()); rc != int(C.SQLITE_OK) {
		return fmt.Errorf("sqlite3_vfs_unregister(trace) rc=%d", rc)
	}
	return nil
}

func pilotTraceReset() { C.pilot_trace_reset() }

func pilotTraceOverflowed() bool { return C.pilot_trace_overflowed() != 0 }

func pilotTraceTestSetOverflowed(overflow bool) {
	value := C.int(0)
	if overflow {
		value = 1
	}
	C.pilot_trace_test_set_overflowed(value)
}

func pilotTraceSetActor(actor string) {
	cActor := C.CString(actor)
	defer C.free(unsafe.Pointer(cActor))
	C.pilot_trace_set_actor(cActor)
}

func pilotTraceSetContext(runID, scenarioID, caseID, sourceID string) {
	cr := C.CString(runID)
	cs := C.CString(scenarioID)
	cc := C.CString(caseID)
	ci := C.CString(sourceID)
	defer C.free(unsafe.Pointer(cr))
	defer C.free(unsafe.Pointer(cs))
	defer C.free(unsafe.Pointer(cc))
	defer C.free(unsafe.Pointer(ci))
	C.pilot_trace_set_context(cr, cs, cc, ci)
}

const pilotTraceBufferCapacity = 4 * 1024 * 1024

func pilotTraceDump() string {
	buf := C.malloc(C.size_t(pilotTraceBufferCapacity))
	if buf == nil {
		panic("allocate immutable trace snapshot")
	}
	defer C.free(buf)
	n := C.pilot_trace_copy((*C.char)(buf), C.int(pilotTraceBufferCapacity))
	if n < 0 {
		panic("copy trace snapshot failed")
	}
	return C.GoStringN((*C.char)(buf), n)
}

func pilotTraceSetPagerBarrier(controlFD, traceFD int, scenarioID string) {
	cScenario := C.CString(scenarioID)
	defer C.free(unsafe.Pointer(cScenario))
	C.pilot_trace_set_pager_barrier(C.int(controlFD), C.int(traceFD), cScenario)
}

func pilotTraceFlushDurable() error {
	if rc := int(C.pilot_trace_flush_durable()); rc != 0 {
		return fmt.Errorf("persist VFS callback trace: rc=%d", rc)
	}
	return nil
}
