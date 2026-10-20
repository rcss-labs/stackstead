package peers

import (
	"context"
	"errors"
	"testing"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func TestAddServerRejectsInvalidInputBeforeAnyNetworkCall(t *testing.T) {
	svc, st := newTestService(t)
	valid := apitypes.AddPeerRequest{
		URL: "https://b.example", Token: "ssc_x", AdvertisedURL: "https://a.example",
	}
	cases := []struct {
		name      string
		tweak     func(*apitypes.AddPeerRequest)
		wantField string
	}{
		{"missing token", func(r *apitypes.AddPeerRequest) { r.Token = "" }, "token"},
		{"not a url", func(r *apitypes.AddPeerRequest) { r.URL = "b.example" }, "url"},
		{"http without acceptance", func(r *apitypes.AddPeerRequest) { r.URL = "http://127.0.0.1:1" }, "trust.plaintext"},
		{"acceptance differs per direction", func(r *apitypes.AddPeerRequest) {
			r.URL = "http://127.0.0.1:1"
			r.Trust.Plaintext = true
		}, "selfTrust.plaintext"},
		{"bad pin", func(r *apitypes.AddPeerRequest) { r.Trust.PinSHA256 = "nothex" }, "trust.pinSha256"},
		{"bad own pin", func(r *apitypes.AddPeerRequest) { r.SelfTrust.PinSHA256 = "nothex" }, "selfTrust.pinSha256"},
		{"no advertised address", func(r *apitypes.AddPeerRequest) { r.AdvertisedURL = "" }, "advertisedUrl"},
		{"advertised address with a query", func(r *apitypes.AddPeerRequest) { r.AdvertisedURL = "https://a.example/?x=1" }, "advertisedUrl"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := valid
			c.tweak(&req)
			_, err := svc.AddServer(context.Background(), req)
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v, want *ValidationError", err)
			}
			if len(verr.Fields) == 0 || verr.Fields[0].Field != c.wantField {
				t.Errorf("fields = %+v, want first field %q", verr.Fields, c.wantField)
			}
			if n, _ := st.CountPeers(context.Background()); n != 0 {
				t.Errorf("%d peers stored after a rejected request", n)
			}
		})
	}
}

func TestAddServerActivatePendingReplacesTemporaryRow(t *testing.T) {
	ctx := context.Background()
	_, st := newTestService(t)
	if err := st.InsertPeer(ctx, Peer{InstanceID: "tmp-1", Name: "b.example", State: StatePending}, HashSecret("ssp_in")); err != nil {
		t.Fatal(err)
	}
	err := st.ActivatePending(ctx, "tmp-1", Peer{InstanceID: "real-b", Name: "mini-02", URL: "https://b.example",
		OutCredential: "ssp_out", State: StateActive}, HashSecret("ssp_in"))
	if err != nil {
		t.Fatal(err)
	}
	list, _ := st.Peers(ctx)
	if len(list) != 1 || list[0].InstanceID != "real-b" || list[0].State != StateActive {
		t.Fatalf("peers = %+v, want only real-b active", list)
	}
	if p, err := st.PeerByInboundHash(ctx, HashSecret("ssp_in")); err != nil || p.InstanceID != "real-b" {
		t.Errorf("inbound hash resolves to %+v, %v", p, err)
	}
	// An already active row for the real ID is refused and the temporary row survives.
	if err := st.InsertPeer(ctx, Peer{InstanceID: "tmp-2", Name: "x", State: StatePending}, HashSecret("ssp_in2")); err != nil {
		t.Fatal(err)
	}
	err = st.ActivatePending(ctx, "tmp-2", Peer{InstanceID: "real-b", Name: "mini-02", State: StateActive}, HashSecret("ssp_in2"))
	if !errors.Is(err, ErrAlreadyPaired) {
		t.Fatalf("err = %v, want ErrAlreadyPaired", err)
	}
	if _, err := st.Peer(ctx, "tmp-2"); err != nil {
		t.Errorf("temporary row lost on a refused activation: %v", err)
	}
}
