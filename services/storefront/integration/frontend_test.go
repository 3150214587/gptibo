package integration_test

import (
	"encoding/json"
	"gptibo/storefront/internal/httpapi"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFrontendRouteContracts(t *testing.T) {
	root, e := filepath.Abs("../../..")
	if e != nil {
		t.Fatal(e)
	}
	routes, _ := json.Marshal(httpapi.RouteInventory())
	file := filepath.Join(t.TempDir(), "routes.json")
	if e = os.WriteFile(file, routes, 0600); e != nil {
		t.Fatal(e)
	}
	command := exec.Command("node", filepath.Join(root, "services/storefront/scripts/check-frontend-contracts.mjs"), root, file)
	out, e := command.CombinedOutput()
	if e != nil {
		t.Fatalf("frontend route contract mismatch: %s", out)
	}
	t.Log(string(out))
}
