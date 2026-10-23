package awsauth

import (
	"strings"
	"testing"
)

func TestParseINI(t *testing.T) {
	src := "# top comment\n; another\n[default]\naws_access_key_id = AKIA\n  Aws_Secret_Access_Key=sec=ret  \n\n[profile x]\nregion = eu-west-1 # not a comment\nno-delimiter-line\n[ y ]\nk = v\n"
	got, err := ParseINI(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if got["default"]["aws_access_key_id"] != "AKIA" || got["default"]["aws_secret_access_key"] != "sec=ret" {
		t.Errorf("default = %v", got["default"])
	}
	if got["profile x"]["region"] != "eu-west-1 # not a comment" {
		t.Errorf("profile x = %v", got["profile x"])
	}
	if got["y"]["k"] != "v" {
		t.Errorf("y = %v", got["y"])
	}
}

func TestParseINIKeyOutsideSection(t *testing.T) {
	if _, err := ParseINI(strings.NewReader("a = b\n")); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseINIColonDelimiter(t *testing.T) {
	src := "[work]\naws_access_key_id: AKIA\naws_secret_access_key : sek\nurl = http://h:80\nodd:key = v=1\n"
	got, err := ParseINI(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	w := got["work"]
	if w["aws_access_key_id"] != "AKIA" || w["aws_secret_access_key"] != "sek" {
		t.Errorf("work = %v", w)
	}
	// The earliest delimiter wins, as in Python configparser.
	if w["url"] != "http://h:80" || w["odd"] != "key = v=1" {
		t.Errorf("work = %v", w)
	}
}
