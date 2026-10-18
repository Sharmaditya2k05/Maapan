package ledger

import (
	"testing"
	"time"
)

func TestBuildTreeRollsUpBalances(t *testing.T) {
	root, debtors := "r", "d"
	flat := []*Account{
		{ID: root, Code: "1000", IsGroup: true},
		{ID: debtors, Code: "1200", IsGroup: true, ParentID: &root},
		{ID: "p1", Code: "P-1", ParentID: &debtors, BalancePaise: 500},
		{ID: "p2", Code: "P-2", ParentID: &debtors, BalancePaise: -200},
		{ID: "c", Code: "1100", ParentID: &root, BalancePaise: 1000},
	}
	tree := buildTree(flat)
	if len(tree) != 1 || tree[0].BalancePaise != 1300 {
		t.Fatalf("root balance %+v", tree[0])
	}
	if tree[0].Children[0].BalancePaise != 300 {
		t.Fatal("debtors group should roll up children")
	}
}

func TestFinancialYear(t *testing.T) {
	cases := map[string]string{"2026-03-31": "25-26", "2026-04-01": "26-27", "2099-12-31": "99-00"}
	for d, want := range cases {
		tm, _ := time.Parse(time.DateOnly, d)
		if got := FinancialYear(tm); got != want {
			t.Errorf("%s: got %s want %s", d, got, want)
		}
	}
}
