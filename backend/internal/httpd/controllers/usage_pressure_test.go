package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/procmem"
)

type fakePressure struct {
	p   procmem.Pressure
	err error
}

func (f fakePressure) Pressure(context.Context) (procmem.Pressure, error) { return f.p, f.err }

func getPressure(t *testing.T, c *UsageController) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	c.Register(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/usage/memory/pressure", nil))
	return rec
}

func TestPressureRouteReturnsTheKernelVerdict(t *testing.T) {
	rec := getPressure(t, &UsageController{Pressure: fakePressure{p: procmem.Pressure{Raw: 2, Source: procmem.PressureSourceMemoryStatus}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var got MemoryPressureResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.PressureRaw != 2 || got.PressureSource != "memorystatus" {
		t.Fatalf("got %+v", got)
	}
}

// An unreadable platform is permanent for the run, so it must read as 501
// (the renderer stops retrying) rather than a 500.
func TestPressureRouteIsNotImplementedWhereUnsupported(t *testing.T) {
	if rec := getPressure(t, &UsageController{Pressure: fakePressure{err: procmem.ErrUnsupported}}); rec.Code != http.StatusNotImplemented {
		t.Fatalf("unsupported: status = %d", rec.Code)
	}
	if rec := getPressure(t, &UsageController{}); rec.Code != http.StatusNotImplemented {
		t.Fatalf("no reader: status = %d", rec.Code)
	}
}
