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

func TestApplyEditsRefusesDeleteOfInheritedKey(t *testing.T) {
	// Deleting a key that only exists through a merge key is a no-op for the
	// text editor, so the decoded result still has it: applyEdits must refuse.
	orig := []byte("x-base: &base\n  restart: always\nservices:\n  web:\n    <<: *base\n    image: nginx\n")
	edits := []edit{{path("services.web.restart"), nil}}
	if _, err := applyEdits(orig, edits); !errors.Is(err, errVerification) {
		t.Errorf("err = %v, want errVerification", err)
	}
}

func TestApplyEditsRefusesMisplacedFlowEnd(t *testing.T) {
	// yaml.v3 ends the comment at U+2028 (a YAML line break), so "y]" closes
	// web.a on line 2 and "b: 1]" is a plain scalar. The text scanner only
	// knows "\n" line breaks, so it reads "1]" as the closing bracket and
	// would delete b too. The result must be refused, not returned.
	orig := []byte("web:\n  a: [x, # n\u2028y]\n  b: 1]\n  c: 1\n")
	edits := []edit{{path("web.a"), nil}}
	if out, err := applyEdits(orig, edits); !errors.Is(err, errVerification) {
		t.Errorf("err = %v, want errVerification; output:\n%s", err, out)
	}
}
