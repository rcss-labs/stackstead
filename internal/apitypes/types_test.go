package apitypes_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func keysOf(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// populated returns a pointer to a zero value of T with every field set to a
// non-zero value, so omitempty tags cannot hide a key.
func populated[T any]() T {
	var v T
	fill(reflect.ValueOf(&v).Elem())
	return v
}

func fill(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint64:
		v.SetUint(1)
	case reflect.Float64:
		v.SetFloat(1)
	case reflect.Ptr:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0))
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(time.Unix(1, 0).UTC()))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			fill(v.Field(i))
		}
	}
}

func TestJSONTagsMatchContract(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"StackSummary", populated[apitypes.StackSummary](), "health,name,origin,path,ports,project,running,serviceCount,status"},
		{"StackDetail", populated[apitypes.StackDetail](), "hash,health,name,origin,parseError,path,ports,project,running,serviceCount,services,status"},
		{"ServiceDetail", populated[apitypes.ServiceDetail](), "config,containers,name,readOnly"},
		{"ServiceConfig", populated[apitypes.ServiceConfig](), "build,environment,healthcheck,image,limits,networkMode,networks,ports,restart,volumes"},
		{"ContainerInfo", populated[apitypes.ContainerInfo](), "health,id,name,service,state,status"},
		{"Build", populated[apitypes.Build](), "context,dockerfile"},
		{"EnvVar", populated[apitypes.EnvVar](), "name,value"},
		{"NetworkAttachment", populated[apitypes.NetworkAttachment](), "ipv4Address,name"},
		{"Limits", populated[apitypes.Limits](), "cpus,memory"},
		{"Healthcheck", populated[apitypes.Healthcheck](), "interval,retries,startPeriod,test,timeout"},
		{"Operation", populated[apitypes.Operation](), "action,endedAt,error,exitCode,id,stack,startedAt,state"},
		{"OperationDetail", populated[apitypes.OperationDetail](), "action,endedAt,error,exitCode,id,lines,stack,startedAt,state"},
		{"Network", populated[apitypes.Network](), "driver,name,subnets"},
		{"FSEntry", populated[apitypes.FSEntry](), "isDir,name,size"},
		{"Status", populated[apitypes.Status](), "compose,docker,events,metricsDegraded,version"},
		{"ErrorBody", populated[apitypes.ErrorBody](), "error,fields"},
		{"FieldError", populated[apitypes.FieldError](), "field,message"},
		{"RawCompose", populated[apitypes.RawCompose](), "content,hash"},
		{"HashResponse", populated[apitypes.HashResponse](), "hash"},
		{"PreviewResponse", populated[apitypes.PreviewResponse](), "content,hash"},
		{"CreateStackRequest", populated[apitypes.CreateStackRequest](), "config,name,service"},
		{"ImportRequest", populated[apitypes.ImportRequest](), "path"},
		{"UpdateServiceRequest", populated[apitypes.UpdateServiceRequest](), "config,hash"},
		{"LogEvent", populated[apitypes.LogEvent](), "container,service,stream,text,time"},
		{"LogError", populated[apitypes.LogError](), "container,error,service"},
		{"MetricPoint", populated[apitypes.MetricPoint](), "blkReadBytesPerSec,blkWriteBytesPerSec,cpu,mem,memLimit,netRxBytesPerSec,netTxBytesPerSec,service,time"},
		{"MetricsHistory", populated[apitypes.MetricsHistory](), "points"},
		{"Sample", populated[apitypes.Sample](), "blkRead,blkWrite,container,cpu,mem,memLimit,netRx,netTx,service,time"},
		{"Change", populated[apitypes.Change](), "project,seq"},
		{"StatusFlags", populated[apitypes.StatusFlags](), "events,metricsDegraded"},
		{"SetupStatus", populated[apitypes.SetupStatus](), "needsSetup"},
		{"SetupRequest", populated[apitypes.SetupRequest](), "password,setupToken,username"},
		{"LoginRequest", populated[apitypes.LoginRequest](), "password,username"},
		{"Me", populated[apitypes.Me](), "username"},
		{"ChangePasswordRequest", populated[apitypes.ChangePasswordRequest](), "current,new"},
		{"MetricsTokenStatus", populated[apitypes.MetricsTokenStatus](), "configured"},
		{"MetricsToken", populated[apitypes.MetricsToken](), "token"},
		{"Health", populated[apitypes.Health](), "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(keysOf(t, tc.v), ",")
			if got != tc.want {
				t.Errorf("keys = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestOperationStatesAndActions(t *testing.T) {
	if got := strings.Join(apitypes.Actions, ","); got != "up,down,restart,pull,build" {
		t.Errorf("Actions = %s", got)
	}
	states := []string{apitypes.OpRunning, apitypes.OpSucceeded, apitypes.OpFailed, apitypes.OpCancelled, apitypes.OpTimedOut}
	if got := strings.Join(states, ","); got != "running,succeeded,failed,cancelled,timed_out" {
		t.Errorf("states = %s", got)
	}
}

const modulePrefix = "github.com/rcss-labs/stackstead/internal/"

func TestNoServerImports(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "list", "-deps", "github.com/rcss-labs/stackstead/internal/apitypes").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasPrefix(pkg, modulePrefix) && pkg != modulePrefix+"apitypes" {
			t.Errorf("apitypes depends on %s", pkg)
		}
	}
}
