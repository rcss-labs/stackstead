package composefile

import (
	"errors"
	"testing"
)

func TestVerifyAcceptsExactChange(t *testing.T) {
	orig := []byte("services:\n  web:\n    image: nginx\n    restart: always\n")
	edits := []edit{
		{path("services.web.image"), scalar("nginx:1.28")},
		{path("services.web.restart"), nil},
		{path("services.web.deploy.resources.limits.memory"), scalar("1G")},
	}
	edited := []byte("services:\n  web:\n    image: nginx:1.28\n    deploy:\n      resources:\n        limits:\n          memory: 1G\n")
	if err := verifyEdits(orig, edited, edits); err != nil {
		t.Errorf("verifyEdits: %v", err)
	}
}

func TestVerifyRejectsCollateralChange(t *testing.T) {
	orig := []byte("services:\n  web:\n    image: nginx\n    restart: always\n")
	edits := []edit{{path("services.web.image"), scalar("nginx:1.28")}}
	edited := []byte("services:\n  web:\n    image: nginx:1.28\n") // restart lost
	if err := verifyEdits(orig, edited, edits); !errors.Is(err, errVerification) {
		t.Errorf("err = %v, want errVerification", err)
	}
}

func TestVerifyRejectsInvalidYAML(t *testing.T) {
	orig := []byte("services: {}\n")
	if err := verifyEdits(orig, []byte("services: [\n"), nil); !errors.Is(err, errVerification) {
		t.Errorf("err = %v, want errVerification", err)
	}
}

func TestVerifyCatchesDeleteOfInheritedKey(t *testing.T) {
	// Deleting a key that only exists through a merge key is a no-op for the
	// text editor, so the decoded result still has it: verification must fail.
	orig := []byte("x-base: &base\n  restart: always\nservices:\n  web:\n    <<: *base\n    image: nginx\n")
	edits := []edit{{path("services.web.restart"), nil}}
	edited, err := applyEdits(orig, edits)
	if err != nil {
		t.Fatalf("applyEdits: %v", err)
	}
	if err := verifyEdits(orig, edited, edits); !errors.Is(err, errVerification) {
		t.Errorf("err = %v, want errVerification", err)
	}
}
