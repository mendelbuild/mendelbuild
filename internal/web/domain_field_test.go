package web

import (
	"os"
	"strings"
	"testing"
)

// TestLabelFieldWidthBeatsTheGeneralFieldRule guards a rule that loses silently.
//
// The general rule is `.field input[type="text"] { width: 100% }`, whose
// specificity is (0,2,1). A two-class selector for the narrow label box is
// (0,2,0) and loses, so the box renders full width -- which is precisely the
// shape that invites a whole domain where one label belongs. Nothing about that
// looks broken; the CSS is present and simply outweighed.
func TestLabelFieldWidthBeatsTheGeneralFieldRule(t *testing.T) {
	css, err := os.ReadFile("static/css/components.css")
	if err != nil {
		t.Fatalf("read stylesheet: %v", err)
	}
	sheet := string(css)

	if !strings.Contains(sheet, ".field .input-suffixed .input-label") {
		t.Error("the narrow label box needs three classes to outweigh " +
			`.field input[type="text"] { width: 100% }; with two it renders full width`)
	}
	if strings.Contains(sheet, "\n.input-suffixed .input-label {") {
		t.Error("a two-class version of the rule is present and will lose to the general field rule")
	}
}
