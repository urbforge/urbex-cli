package bootstrap

import (
	"regexp"
	"strings"
	"testing"
)

func TestReconcileScript_IsTheLogicWithoutExportsThenTheDriver(t *testing.T) {
	script, err := ReconcileScript()
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?m)^export `).MatchString(script) {
		t.Error("an Action is not a module: no export may be left")
	}
	logic, driver := strings.Index(script, "function decide("), strings.Index(script, "await main();")
	if logic < 0 || driver < 0 || logic > driver {
		t.Errorf("expected the logic, then the driver; decide at %d, main at %d", logic, driver)
	}
	if !strings.Contains(script, `"`+ReconcileStateVariable+`"`) {
		t.Errorf("the driver should keep its state in %s", ReconcileStateVariable)
	}
}

func TestGraphVariable(t *testing.T) {
	if got := GraphVariable("my-shop", "prod"); got != "URBEX_GRAPH_MY_SHOP_PROD" {
		t.Errorf("GraphVariable = %q", got)
	}
}
