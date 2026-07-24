package knowledge

import "testing"

func TestPlanQueriesExtractsSymbolsAndTokens(t *testing.T) {
	plan := PlanQueries("怎么配置 `work_mode` 和 internal/config/config.go 的 LoadConfig")
	want := []string{"怎么配置 `work_mode` 和 internal/config/config.go 的 LoadConfig", "work_mode", "internal/config/config.go", "LoadConfig"}
	for _, q := range want {
		if !hasQuery(plan.Queries, q) {
			t.Fatalf("missing query %q in %#v", q, plan.Queries)
		}
	}
	if len(plan.Queries) > 5 {
		t.Fatalf("too many derived queries: %#v", plan.Queries)
	}
}

func TestPlanQueriesDedupesCaseInsensitive(t *testing.T) {
	plan := PlanQueries("LoadConfig loadconfig `LoadConfig`")
	count := 0
	for _, q := range plan.Queries {
		if q == "LoadConfig" || q == "loadconfig" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one LoadConfig variant, got %#v", plan.Queries)
	}
}

func TestPlanQueriesEmpty(t *testing.T) {
	plan := PlanQueries("   ")
	if plan.Original != "" || len(plan.Queries) != 0 {
		t.Fatalf("empty plan = %#v", plan)
	}
}

func hasQuery(list []string, q string) bool {
	for _, v := range list {
		if v == q {
			return true
		}
	}
	return false
}
