package peers

import (
	"context"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func TestHelloAdvertisesFeatures(t *testing.T) {
	r := newCheckRig(t, helloHandler(goodHello()), false)
	hello, err := r.svc.Hello(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{FeatureYAMLCreate, FeatureManagedDockerfile}
	if !slices.Equal(hello.Features, want) {
		t.Fatalf("hello features = %v, want %v", hello.Features, want)
	}
}

func TestViewCarriesFeaturesFromLastHello(t *testing.T) {
	hello := goodHello()
	hello.Features = []string{FeatureYAMLCreate}
	var current atomic.Pointer[apitypes.PeerHello]
	current.Store(&hello)
	r := newCheckRig(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		helloHandler(*current.Load()).ServeHTTP(w, req)
	}), true)

	featuresOf := func() []string {
		t.Helper()
		views, err := r.svc.Views(context.Background())
		if err != nil || len(views) != 1 {
			t.Fatalf("views = %+v, err %v", views, err)
		}
		return views[0].Features
	}
	if got := featuresOf(); len(got) != 0 || got == nil {
		t.Fatalf("before any hello features = %#v, want an empty non-nil list", got)
	}
	if _, err := r.svc.CheckNow(context.Background(), "b-id"); err != nil {
		t.Fatal(err)
	}
	if got := featuresOf(); !slices.Equal(got, []string{FeatureYAMLCreate}) {
		t.Fatalf("features = %v", got)
	}
	// A peer that now advertises nothing (an older version) loses them.
	old := goodHello()
	current.Store(&old)
	if _, err := r.svc.CheckNow(context.Background(), "b-id"); err != nil {
		t.Fatal(err)
	}
	if got := featuresOf(); len(got) != 0 || got == nil {
		t.Fatalf("after an old hello features = %#v, want an empty non-nil list", got)
	}
}
