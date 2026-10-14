package commerce

// UpstreamPlan is the public subset of a supplier's GPT subscription catalog.
// Code is the registry key accepted by orders, not its internal acc_plan_key.
type UpstreamPlan struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Purchasable bool   `json:"purchasable"`
}
