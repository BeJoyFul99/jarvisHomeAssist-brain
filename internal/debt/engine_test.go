package debt

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

// fixedStart pins the debt-free date so assertions don't drift with the clock.
var fixedStart = time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)

func base(in Input) Input {
	in.StartDate = fixedStart
	return in
}

// TestSpecExample reproduces the scenario from the product spec:
// $6,250 debt, $3,200 income, ~$700/month allocated, ~9 months to freedom.
func TestSpecExample(t *testing.T) {
	p := Build(base(Input{
		MonthlyIncome: 3200,
		FixedExpenses: 2400,
		Debts: []Debt{
			{Name: "Credit Card", Balance: 4000, APR: 19.99, MinPayment: 120},
			{Name: "Personal Loan", Balance: 2250, APR: 8.5, MinPayment: 90},
		},
	}))

	if !p.Feasible {
		t.Fatalf("expected a feasible plan, got warnings: %v", p.Warnings)
	}
	if p.TotalDebt != 6250 {
		t.Errorf("TotalDebt = %v, want 6250", p.TotalDebt)
	}
	// Disposable 800, minimums 210, surplus 590, buffer 59 → target 741.
	if got, want := p.MonthlyPaymentTarget, 741.0; math.Abs(got-want) > 0.01 {
		t.Errorf("MonthlyPaymentTarget = %v, want %v", got, want)
	}
	if p.MonthsToFreedom < 8 || p.MonthsToFreedom > 10 {
		t.Errorf("MonthsToFreedom = %d, want ~9", p.MonthsToFreedom)
	}
	if p.DebtFreeDate == "" {
		t.Error("expected a debt-free date")
	}
	if p.InterestSaved <= 0 {
		t.Errorf("InterestSaved = %v, want > 0", p.InterestSaved)
	}
	t.Logf("summary: %s", p.Summary)
}

// A zero-interest debt is pure division: balance / payment, rounded up.
func TestZeroInterestIsExactDivision(t *testing.T) {
	p := Build(base(Input{
		MonthlyIncome: 2000,
		FixedExpenses: 1000,
		// Disposable 1000, minimums 100, surplus 900, buffer 90 → target 910.
		Debts: []Debt{{Name: "Family Loan", Balance: 9100, APR: 0, MinPayment: 100}},
	}))

	if got, want := p.MonthlyPaymentTarget, 910.0; math.Abs(got-want) > 0.01 {
		t.Fatalf("MonthlyPaymentTarget = %v, want %v", got, want)
	}
	if p.MonthsToFreedom != 10 {
		t.Errorf("MonthsToFreedom = %d, want 10 (9100/910)", p.MonthsToFreedom)
	}
	if p.TotalInterest != 0 {
		t.Errorf("TotalInterest = %v, want 0 on a 0%% APR debt", p.TotalInterest)
	}
}

// Avalanche must target the highest rate first regardless of balance size;
// snowball must target the smallest balance first regardless of rate.
func TestStrategyOrdering(t *testing.T) {
	debts := []Debt{
		{Name: "Big Cheap", Balance: 10000, APR: 5, MinPayment: 100},
		{Name: "Small Pricey", Balance: 1000, APR: 25, MinPayment: 50},
	}

	av := Build(base(Input{MonthlyIncome: 4000, FixedExpenses: 2000, Debts: debts, Strategy: Avalanche}))
	if av.Order[0].Name != "Small Pricey" {
		t.Errorf("avalanche first = %q, want Small Pricey (25%% APR)", av.Order[0].Name)
	}

	sn := Build(base(Input{MonthlyIncome: 4000, FixedExpenses: 2000, Debts: debts, Strategy: Snowball}))
	if sn.Order[0].Name != "Small Pricey" {
		t.Errorf("snowball first = %q, want Small Pricey (smallest balance)", sn.Order[0].Name)
	}

	// Now make the orderings genuinely disagree.
	conflicting := []Debt{
		{Name: "Big Pricey", Balance: 10000, APR: 25, MinPayment: 200},
		{Name: "Small Cheap", Balance: 800, APR: 3, MinPayment: 40},
	}
	av2 := Build(base(Input{MonthlyIncome: 4000, FixedExpenses: 2000, Debts: conflicting, Strategy: Avalanche}))
	if av2.Order[0].Name != "Big Pricey" {
		t.Errorf("avalanche first = %q, want Big Pricey", av2.Order[0].Name)
	}
	sn2 := Build(base(Input{MonthlyIncome: 4000, FixedExpenses: 2000, Debts: conflicting, Strategy: Snowball}))
	if sn2.Order[0].Name != "Small Cheap" {
		t.Errorf("snowball first = %q, want Small Cheap", sn2.Order[0].Name)
	}

	// Avalanche is never more expensive than snowball — that's its whole point.
	if av2.TotalInterest > sn2.TotalInterest+0.01 {
		t.Errorf("avalanche interest %v exceeded snowball %v", av2.TotalInterest, sn2.TotalInterest)
	}
}

// Income below essentials is the real rescue case: report the gap plainly
// rather than emitting a fantasy payoff date.
func TestShortfallIsReportedNotHidden(t *testing.T) {
	p := Build(base(Input{
		MonthlyIncome: 1500,
		FixedExpenses: 1400,
		Debts: []Debt{
			{Name: "Card A", Balance: 5000, APR: 22, MinPayment: 150},
			{Name: "Card B", Balance: 3000, APR: 18, MinPayment: 90},
		},
	}))

	if p.Feasible {
		t.Error("expected Feasible=false when income can't cover essentials")
	}
	// Essentials 1400 + 240 = 1640 against 1500 income → 140 short.
	if got, want := p.Survival.Shortfall, 140.0; math.Abs(got-want) > 0.01 {
		t.Errorf("Shortfall = %v, want %v", got, want)
	}
	if p.DebtFreeDate != "" {
		t.Errorf("DebtFreeDate = %q, want empty on an infeasible plan", p.DebtFreeDate)
	}
	if len(p.Warnings) == 0 {
		t.Error("expected a warning explaining the shortfall")
	}
	// The order is still useful — it tells them what to renegotiate first.
	if len(p.Order) != 2 {
		t.Errorf("len(Order) = %d, want 2 even when infeasible", len(p.Order))
	}
}

// A minimum below the monthly interest means the balance grows forever.
// The engine must warn instead of silently looping to the horizon.
func TestMinimumBelowInterestWarns(t *testing.T) {
	p := Build(base(Input{
		MonthlyIncome: 3000,
		FixedExpenses: 1000,
		// 10000 at 24% accrues 200/month; a 50 minimum never touches principal.
		Debts: []Debt{{Name: "Trap Card", Balance: 10000, APR: 24, MinPayment: 50}},
	}))

	if len(p.Warnings) == 0 {
		t.Fatal("expected a warning that the minimum doesn't cover interest")
	}
	// The accelerated plan still clears it, because the target is far above
	// the minimum — that contrast is exactly the tool's value.
	if !p.Feasible {
		t.Errorf("expected the accelerated plan to still clear the debt: %v", p.Warnings)
	}
	if !p.BaselineCapped {
		t.Error("expected BaselineCapped=true — at minimums this debt never ends")
	}
}

// When the payment genuinely can't outrun the interest, converge=false rather
// than reporting a 600-month "plan".
func TestNonConvergingPlanIsInfeasible(t *testing.T) {
	p := Build(base(Input{
		MonthlyIncome: 1000,
		FixedExpenses: 800,
		// Disposable 200, minimum 10. Target ~181 vs ~1000/month of interest.
		Debts: []Debt{{Name: "Runaway", Balance: 50000, APR: 24, MinPayment: 10}},
	}))

	if p.Feasible {
		t.Error("expected Feasible=false when interest outruns the payment")
	}
	if p.DebtFreeDate != "" {
		t.Errorf("DebtFreeDate = %q, want empty", p.DebtFreeDate)
	}
	if p.MonthsToFreedom > maxMonths {
		t.Errorf("MonthsToFreedom = %d exceeded the %d-month cap", p.MonthsToFreedom, maxMonths)
	}
}

// Available cash keeps one month of essentials in reserve; only the excess is
// deployed against the debt.
func TestEmergencyReserveHeldBackFromLumpSum(t *testing.T) {
	in := base(Input{
		MonthlyIncome: 3000,
		FixedExpenses: 1500,
		AvailableCash: 5000,
		Debts:         []Debt{{Name: "Card", Balance: 6000, APR: 20, MinPayment: 150}},
	})
	p := Build(in)

	// Essentials = 1500 + 150 = 1650 held back; 3350 deployed.
	if got, want := p.Survival.EmergencyReserve, 1650.0; math.Abs(got-want) > 0.01 {
		t.Errorf("EmergencyReserve = %v, want %v", got, want)
	}
	if got, want := p.Survival.LumpSumDeployed, 3350.0; math.Abs(got-want) > 0.01 {
		t.Errorf("LumpSumDeployed = %v, want %v", got, want)
	}

	// The lump sum must actually shorten the payoff.
	noCash := in
	noCash.AvailableCash = 0
	if slower := Build(noCash); slower.MonthsToFreedom <= p.MonthsToFreedom {
		t.Errorf("lump sum didn't help: %d months with cash vs %d without",
			p.MonthsToFreedom, slower.MonthsToFreedom)
	}
}

// Cash below one month of essentials is entirely reserved — nothing deployed.
func TestThinCashIsFullyReserved(t *testing.T) {
	p := Build(base(Input{
		MonthlyIncome: 3000,
		FixedExpenses: 1500,
		AvailableCash: 400,
		Debts:         []Debt{{Name: "Card", Balance: 6000, APR: 20, MinPayment: 150}},
	}))

	if got, want := p.Survival.EmergencyReserve, 400.0; math.Abs(got-want) > 0.01 {
		t.Errorf("EmergencyReserve = %v, want the full %v", got, want)
	}
	if p.Survival.LumpSumDeployed != 0 {
		t.Errorf("LumpSumDeployed = %v, want 0", p.Survival.LumpSumDeployed)
	}
}

// Interest saved is measured against doing nothing (minimums, no rollover),
// and the accelerated plan must beat that baseline on both time and money.
func TestInterestSavedBeatsBaseline(t *testing.T) {
	p := Build(base(Input{
		MonthlyIncome: 4000,
		FixedExpenses: 2000,
		Debts: []Debt{
			{Name: "Card A", Balance: 8000, APR: 22, MinPayment: 200},
			{Name: "Card B", Balance: 4000, APR: 15, MinPayment: 100},
		},
	}))

	if !p.Feasible {
		t.Fatalf("expected feasible: %v", p.Warnings)
	}
	if p.BaselineInterest <= p.TotalInterest {
		t.Errorf("baseline interest %v should exceed plan interest %v", p.BaselineInterest, p.TotalInterest)
	}
	if got := p.BaselineInterest - p.TotalInterest; math.Abs(got-p.InterestSaved) > 0.01 {
		t.Errorf("InterestSaved = %v, want %v", p.InterestSaved, got)
	}
	if p.BaselineMonths <= p.MonthsToFreedom {
		t.Errorf("baseline %d months should exceed plan %d months", p.BaselineMonths, p.MonthsToFreedom)
	}
	if p.MonthsSaved != p.BaselineMonths-p.MonthsToFreedom {
		t.Errorf("MonthsSaved = %d, want %d", p.MonthsSaved, p.BaselineMonths-p.MonthsToFreedom)
	}
}

// Freed-up minimums must roll into the next debt — that's what makes the
// avalanche accelerate rather than stay linear.
func TestRolloverAcceleratesPayoff(t *testing.T) {
	p := Build(base(Input{
		MonthlyIncome: 3000,
		FixedExpenses: 1500,
		Debts: []Debt{
			{Name: "Small", Balance: 500, APR: 10, MinPayment: 50},
			{Name: "Large", Balance: 3000, APR: 10, MinPayment: 100},
		},
		Strategy: Snowball,
	}))

	if !p.Feasible {
		t.Fatalf("expected feasible: %v", p.Warnings)
	}
	small, large := p.Order[0], p.Order[1]
	if small.Name != "Small" {
		t.Fatalf("snowball order[0] = %q, want Small", small.Name)
	}
	if small.PayoffMonth == 0 || large.PayoffMonth == 0 {
		t.Fatal("both debts should have a payoff month")
	}
	if small.PayoffMonth > large.PayoffMonth {
		t.Errorf("small paid off month %d after large month %d", small.PayoffMonth, large.PayoffMonth)
	}
	// 3500 total at ~1350/month must clear in well under 5 months.
	if p.MonthsToFreedom > 4 {
		t.Errorf("MonthsToFreedom = %d, want <= 4 with rollover", p.MonthsToFreedom)
	}
}

// An explicit override replaces the computed target, and an override below the
// combined minimums is called out rather than accepted silently.
func TestPaymentOverride(t *testing.T) {
	p := Build(base(Input{
		MonthlyIncome:   5000,
		FixedExpenses:   2000,
		PaymentOverride: 400,
		Debts:           []Debt{{Name: "Card", Balance: 4000, APR: 12, MinPayment: 100}},
	}))
	if p.MonthlyPaymentTarget != 400 {
		t.Errorf("MonthlyPaymentTarget = %v, want the 400 override", p.MonthlyPaymentTarget)
	}

	low := Build(base(Input{
		MonthlyIncome:   5000,
		FixedExpenses:   2000,
		PaymentOverride: 50,
		Debts: []Debt{
			{Name: "A", Balance: 4000, APR: 12, MinPayment: 100},
			{Name: "B", Balance: 2000, APR: 12, MinPayment: 80},
		},
	}))
	if len(low.Warnings) == 0 {
		t.Error("expected a warning when the override is below combined minimums")
	}
}

// No debts is a valid, happy state — not a crash or a nonsense plan.
func TestNoDebts(t *testing.T) {
	p := Build(base(Input{MonthlyIncome: 3000, FixedExpenses: 1500}))
	if !p.Feasible {
		t.Error("expected Feasible=true with no debts")
	}
	if p.MonthsToFreedom != 0 || p.TotalDebt != 0 {
		t.Errorf("got %d months / %v debt, want zeroes", p.MonthsToFreedom, p.TotalDebt)
	}
	if p.Summary == "" {
		t.Error("expected a summary even with no debts")
	}
}

// Zero-balance entries are dropped rather than cluttering the payoff order.
func TestClearedDebtsAreDropped(t *testing.T) {
	p := Build(base(Input{
		MonthlyIncome: 3000,
		FixedExpenses: 1500,
		Debts: []Debt{
			{Name: "Paid Off", Balance: 0, APR: 20, MinPayment: 50},
			{Name: "Active", Balance: 1000, APR: 20, MinPayment: 50},
		},
	}))
	if len(p.Order) != 1 || p.Order[0].Name != "Active" {
		t.Errorf("Order = %+v, want only Active", p.Order)
	}
}

// The schedule must track the simulation exactly: one row per month, ending at
// a zero balance, with interest summing to the reported total.
func TestScheduleMatchesSimulation(t *testing.T) {
	p := Build(base(Input{
		MonthlyIncome: 3500,
		FixedExpenses: 2000,
		Debts:         []Debt{{Name: "Card", Balance: 5000, APR: 18, MinPayment: 125}},
	}))

	if !p.Feasible {
		t.Fatalf("expected feasible: %v", p.Warnings)
	}
	if len(p.Schedule) != p.MonthsToFreedom {
		t.Errorf("len(Schedule) = %d, want %d", len(p.Schedule), p.MonthsToFreedom)
	}
	last := p.Schedule[len(p.Schedule)-1]
	if last.Balance > epsilon {
		t.Errorf("final scheduled balance = %v, want 0", last.Balance)
	}

	var sum float64
	for _, row := range p.Schedule {
		sum += row.Interest
	}
	if math.Abs(sum-p.TotalInterest) > 0.05 {
		t.Errorf("schedule interest %v != reported total %v", sum, p.TotalInterest)
	}

	// Balances must fall monotonically once the plan is converging.
	for i := 1; i < len(p.Schedule); i++ {
		if p.Schedule[i].Balance > p.Schedule[i-1].Balance {
			t.Errorf("balance rose at month %d: %v → %v",
				p.Schedule[i].Month, p.Schedule[i-1].Balance, p.Schedule[i].Balance)
		}
	}
}

// The comparison block always covers both strategies so the UI can show the
// cost of picking motivation over arithmetic.
func TestComparisonCoversBothStrategies(t *testing.T) {
	p := Build(base(Input{
		MonthlyIncome: 4000,
		FixedExpenses: 2000,
		Debts: []Debt{
			{Name: "A", Balance: 6000, APR: 24, MinPayment: 150},
			{Name: "B", Balance: 1200, APR: 6, MinPayment: 50},
		},
	}))

	if len(p.Comparison) != 2 {
		t.Fatalf("len(Comparison) = %d, want 2", len(p.Comparison))
	}
	seen := map[string]bool{}
	for _, c := range p.Comparison {
		seen[c.Strategy] = true
		if c.Converged && c.DebtFreeDate == "" {
			t.Errorf("%s converged but has no debt-free date", c.Strategy)
		}
	}
	if !seen[Avalanche] || !seen[Snowball] {
		t.Errorf("Comparison missing a strategy: %+v", p.Comparison)
	}
}

// The debt-free date must be derived from the start date, not the wall clock.
func TestDebtFreeDateAnchoredToStart(t *testing.T) {
	p := Build(base(Input{
		MonthlyIncome: 2000,
		FixedExpenses: 1000,
		Debts:         []Debt{{Name: "Loan", Balance: 1820, APR: 0, MinPayment: 100}},
	}))

	if p.MonthsToFreedom != 2 {
		t.Fatalf("MonthsToFreedom = %d, want 2 (1820 at 910/month)", p.MonthsToFreedom)
	}
	if want := "2026-03"; p.DebtFreeDate != want {
		t.Errorf("DebtFreeDate = %q, want %q (Jan 2026 + 2 months)", p.DebtFreeDate, want)
	}
}

// Regression: a nil Go slice marshals to JSON `null`, and the browser then
// crashes reading `.length` off it. Assert on the marshalled JSON rather than
// len() — len(nil) is 0 in Go, which is exactly why this slipped through.
func TestPlanSlicesNeverMarshalToNull(t *testing.T) {
	scenarios := map[string]Input{
		"healthy plan (no warnings)": {
			MonthlyIncome: 3200,
			FixedExpenses: 2400,
			Debts: []Debt{
				{Name: "Credit Card", Balance: 4000, APR: 19.99, MinPayment: 120},
				{Name: "Personal Loan", Balance: 2250, APR: 8.5, MinPayment: 90},
			},
		},
		"no debts at all": {MonthlyIncome: 3000, FixedExpenses: 1500},
		"shortfall": {
			MonthlyIncome: 1500,
			FixedExpenses: 1400,
			Debts:         []Debt{{Name: "Card", Balance: 5000, APR: 22, MinPayment: 240}},
		},
		"non-converging": {
			MonthlyIncome: 1000,
			FixedExpenses: 800,
			Debts:         []Debt{{Name: "Runaway", Balance: 50000, APR: 24, MinPayment: 10}},
		},
	}

	for name, in := range scenarios {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(Build(base(in)))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var doc map[string]json.RawMessage
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			for _, key := range []string{"warnings", "order", "comparison", "schedule"} {
				v, ok := doc[key]
				if !ok {
					t.Errorf("%s missing from the payload", key)
					continue
				}
				if string(v) == "null" {
					t.Errorf("%s marshalled to null — the UI reads .length off it and crashes", key)
				}
			}
		})
	}
}

func TestMoneyFormatting(t *testing.T) {
	// Expectations mirror Intl.NumberFormat("en-US", { currencyDisplay:
	// "narrowSymbol" }) — the exact formatter the UI uses. See
	// jarvishomeassist-face/lib/currency.ts.
	cases := []struct {
		in   float64
		code string
		want string
	}{
		{0, "USD", "$0"},
		{700, "USD", "$700"},
		{1234.5, "USD", "$1,234.50"},
		{6250, "USD", "$6,250"},
		{1234567.89, "USD", "$1,234,567.89"},
		{-140, "USD", "-$140"},
		// Multi-character symbols take a space, matching Intl ("RM 6,250").
		{6250, "MYR", "RM" + nbsp + "6,250"},
		{-2.7, "MYR", "-RM" + nbsp + "2.70"},
		{1234.5, "EUR", "€1,234.50"},
		// Intl renders SGD as a bare "$" under narrowSymbol.
		{-140, "SGD", "-$140"},
		// Yen has no minor unit — it must never show cents.
		{1234.5, "JPY", "¥1,235"},
		{6250, "JPY", "¥6,250"},
	}
	for _, c := range cases {
		if got := money(c.in, formatFor(c.code)); got != c.want {
			t.Errorf("money(%v, %s) = %q, want %q", c.in, c.code, got, c.want)
		}
	}
}

// The prose the engine writes must carry the requested currency's symbol —
// a plan rendered in RM alongside a "$700" summary reads as a bug.
func TestCurrencyFlowsIntoProse(t *testing.T) {
	in := base(Input{
		MonthlyIncome: 3200,
		FixedExpenses: 2400,
		Currency:      "MYR",
		Debts: []Debt{
			{Name: "Credit Card", Balance: 4000, APR: 19.99, MinPayment: 120},
			{Name: "Personal Loan", Balance: 2250, APR: 8.5, MinPayment: 90},
		},
	})
	p := Build(in)

	if p.Currency != "MYR" {
		t.Errorf("Currency = %q, want MYR", p.Currency)
	}
	if !strings.Contains(p.Summary, "RM") {
		t.Errorf("summary should carry the RM symbol: %q", p.Summary)
	}
	if strings.Contains(p.Summary, "$") {
		t.Errorf("summary should not carry a $ when currency is MYR: %q", p.Summary)
	}

	// Warnings too — check the shortfall path, which writes its own prose.
	short := Build(base(Input{
		MonthlyIncome: 1500,
		FixedExpenses: 1400,
		Currency:      "MYR",
		Debts:         []Debt{{Name: "Card", Balance: 5000, APR: 22, MinPayment: 240}},
	}))
	if len(short.Warnings) == 0 {
		t.Fatal("expected a shortfall warning")
	}
	joined := strings.Join(short.Warnings, " ") + short.Summary
	if !strings.Contains(joined, "RM") || strings.Contains(joined, "$") {
		t.Errorf("shortfall prose should use RM and no $: %q", joined)
	}
}

// An unknown or empty currency must fall back to USD rather than emitting
// an empty symbol or a bare number.
func TestUnknownCurrencyFallsBackToDollar(t *testing.T) {
	for _, code := range []string{"", "   ", "ZZZ", "not-a-code"} {
		p := Build(base(Input{
			MonthlyIncome: 3000,
			FixedExpenses: 1500,
			Currency:      code,
			Debts:         []Debt{{Name: "Card", Balance: 1000, APR: 10, MinPayment: 50}},
		}))
		if !strings.Contains(p.Summary, "$") {
			t.Errorf("currency %q: expected a $ fallback in %q", code, p.Summary)
		}
	}

	// A recognised code is normalised regardless of case/whitespace.
	p := Build(base(Input{
		MonthlyIncome: 3000,
		FixedExpenses: 1500,
		Currency:      " myr ",
		Debts:         []Debt{{Name: "Card", Balance: 1000, APR: 10, MinPayment: 50}},
	}))
	if p.Currency != "MYR" {
		t.Errorf("Currency = %q, want MYR", p.Currency)
	}
	if !strings.Contains(p.Summary, "RM") {
		t.Errorf("expected RM in %q", p.Summary)
	}
}
